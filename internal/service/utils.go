package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"hash/fnv"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"math/rand"
	"net/http"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sort"
	"strings"
	"time"
)

var (
	dummyAgentNames            = make(map[string][]string) // maps from AutoScaledAgent.Name to the list of dummy agent names
	cachedCustomResourceHash   = make(map[string]string)   // maps from AutoScaledAgent.Name to a stringified hash of the CR data
	lastDeadDummyAgentDeletion = time.Date(1999, 1, 1, 0, 0, 0, 0, time.Local)
	cachedAzpPoolIds           = make(map[string]int64)
)

func CreateHTTPClient() *http.Client {
	// Configure default timeout
	timeout := 2 * time.Second
	httpClient := &http.Client{
		Timeout: timeout,
	}
	return httpClient
}

func GetPendingJobs(ctx context.Context, poolId int64, azurePat string, httpClient *http.Client,
	spec *apscalerv1.AutoScaledAgentSpec) (*PendingJobsWrapper, error) {

	url := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/jobrequests", spec.OrganizationUrl, poolId)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth("", azurePat)
	if err != nil {
		return nil, err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	b, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d response: %s", url, response.StatusCode, string(b))
	}

	var jobRequestsFromApi AzurePipelinesApiJobRequests
	err = json.Unmarshal(b, &jobRequestsFromApi)
	if err != nil {
		return nil, err
	}

	if IsVerboseDebugLoggingEnabled() {
		fmt.Printf("Jobs (raw JSON response): %s\n================\n", b)
	}

	pendingJobs := PendingJobsWrapper{}

	for _, jobRequestFromApi := range jobRequestsFromApi.Value {
		if jobRequestFromApi.Result != nil {
			continue // Job has already completed
		}

		pendingJobs.AddJobRequest(&jobRequestFromApi)
	}

	if IsVerboseDebugLoggingEnabled() {
		fmt.Printf("Pending jobs after parsing: %#v\n================\n", pendingJobs)
	}

	return &pendingJobs, nil
}

// CreateOrUpdateDummyAgents registers one dummy agent for each
// PodsWithCapabilities, as otherwise Azure DevOps would immediately abort a
// pipeline
func CreateOrUpdateDummyAgents(ctx context.Context, poolId int64, azurePat string, httpClient *http.Client,
	crName string, spec *apscalerv1.AutoScaledAgentSpec) ([]string, error) {
	logger := log.FromContext(ctx)

	crHash, err := ComputeCustomResourceHash(spec)
	if err != nil {
		return nil, err
	}

	returnCachedResult := true
	previouslyKnownCrHash, exists := cachedCustomResourceHash[crName]
	if !exists || previouslyKnownCrHash != crHash {
		returnCachedResult = false
	}

	if returnCachedResult {
		return dummyAgentNames[crName], nil
	}

	var newDummyAgentNames []string

	url := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/agents?api-version=7.0", spec.OrganizationUrl, poolId)

	for _, podsWithCapabilities := range spec.PodsWithCapabilities {
		dummyAgentName := getDummyAgentName(&podsWithCapabilities.Capabilities)

		requestBodyTemplate := `{
			"name": "%s",
			"version": "99.999.9",
			"osDescription": "Linux 5.15.49-linuxkit-pr #1 SMP PREEMPT Thu May 25 07:27:39 UTC 2023",
			"enabled": true,
			"status": "offline",
			"provisioningState": "Provisioned",
			"systemCapabilities": %s
		}`
		capabilitiesJsonStr, err := json.Marshal(podsWithCapabilities.Capabilities)
		if err != nil {
			return nil, err
		}

		requestBody := fmt.Sprintf(requestBodyTemplate, dummyAgentName, capabilitiesJsonStr)

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte(requestBody)))
		if err != nil {
			return nil, err
		}

		req.SetBasicAuth("", azurePat)
		if err != nil {
			return nil, err
		}

		req.Header.Set("Content-Type", "application/json")

		response, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		newDummyAgentNames = append(newDummyAgentNames, dummyAgentName)

		if response.StatusCode == 409 {
			continue // 409 = HTTP "conflict", because an agent with the given name already exists, which is fine (EAFP)
		}

		if response.StatusCode != 200 {
			return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
		}
	}

	logger.Info("Successfully created/updated dummy agents", "dummyAgentNames", newDummyAgentNames)

	dummyAgentNames[crName] = newDummyAgentNames
	cachedCustomResourceHash[crName] = crHash

	return newDummyAgentNames, nil
}

func ComputeCustomResourceHash(spec *apscalerv1.AutoScaledAgentSpec) (string, error) {
	var b bytes.Buffer

	// Encode AutoScaledAgentSpec as byte array
	err := gob.NewEncoder(&b).Encode(&spec)
	if err != nil {
		return "", err
	}

	// Generate the hash from the byte array
	h := sha256.New()
	h.Write(b.Bytes())
	hash := h.Sum(nil)
	return fmt.Sprintf("%x", hash), nil
}

func getDummyAgentName(capabilities *map[string]string) string {
	if len(*capabilities) == 0 {
		return DummyAgentNamePrefix
	}
	return fmt.Sprintf("%s-%s", DummyAgentNamePrefix, ComputeMapHash(capabilities))
}

// DeleteDeadDummyAgents deletes all those registered Azure DevOps agents that
// have an "offline" status, have a name starting with <DummyAgentNamePrefix> and
// that have been created some time ago (i.e., any AZP job that needs a
// registered agent is very likely to already have started).
// DeleteDeadDummyAgents performs actual API calls only in regular intervals, to
// avoid spamming the AZP API.
func DeleteDeadDummyAgents(ctx context.Context, poolId int64, azurePat string, httpClient *http.Client,
	spec *apscalerv1.AutoScaledAgentSpec, crName string, dummyAgentsToKeep []string) error {
	if time.Now().Add(-1 * spec.DummyAgentGarbageCollectionInterval.Duration).Before(lastDeadDummyAgentDeletion) {
		return nil
	}

	logger := log.FromContext(ctx)

	url := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/agents?api-version=7.0", spec.OrganizationUrl, poolId)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	req.SetBasicAuth("", azurePat)
	if err != nil {
		return err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	b, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d response: %s", url, response.StatusCode, string(b))
	}

	var agentListFromApi AzurePipelinesAgentList
	err = json.Unmarshal(b, &agentListFromApi)
	if err != nil {
		return err
	}

	for _, agent := range agentListFromApi.Value {
		if containsString(dummyAgentsToKeep, agent.Name) || agent.Status != "offline" {
			continue
		}

		// Delete dummy agents
		if strings.HasPrefix(agent.Name, DummyAgentNamePrefix) {
			cutOffDate := time.Now().Add(-1 * spec.DummyAgentDeletionMinAge.Duration)
			if agent.CreatedOn.Before(cutOffDate) {
				err := DeleteAgent(ctx, spec.OrganizationUrl, poolId, azurePat, httpClient, agent.Id)
				if err != nil {
					return err
				}
				logger.Info("DeleteDeadDummyAgents() deleted offline dummy agent", "agentName", agent.Name)
			}
		}

		// Delete "normal" agents that are offline for a while for some reasons, most likely because K8s killed them
		if strings.HasPrefix(agent.Name, crName) {
			cutOffDate := time.Now().Add(-1 * spec.NormalOfflineAgentDeletionMinAge.Duration)
			if agent.CreatedOn.Before(cutOffDate) {
				err := DeleteAgent(ctx, spec.OrganizationUrl, poolId, azurePat, httpClient, agent.Id)
				if err != nil {
					return err
				}
				logger.Info("DeleteDeadDummyAgents() deleted 'normal' offline agent", "agentName", agent.Name)
			}
		}
	}

	lastDeadDummyAgentDeletion = time.Now()

	logger.Info("Successfully completed garbage collection of dead agents")

	return nil
}

func DeleteAgent(ctx context.Context, organizationUrl string, poolId int64, azurePat string, httpClient *http.Client, agentId int) error {
	url := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/agents/%d?api-version=7.0", organizationUrl, poolId, agentId)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	req.SetBasicAuth("", azurePat)
	if err != nil {
		return err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	b, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d response: %s", url, response.StatusCode, string(b))
	}

	return nil
}

func ComputeMapHash(capabilities *map[string]string) string {
	// Sort keys for consistency
	var keys []string
	for k := range *capabilities {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Create and initialize the FNV-1a hash
	hash := fnv.New64a()
	for _, k := range keys {
		hash.Write([]byte(k))
		hash.Write([]byte((*capabilities)[k]))
	}

	// Convert hash to a 10-character string
	hashValue := hash.Sum(nil)
	return fmt.Sprintf("%010x", hashValue)
}

func GetFilteredRunningPods(pods []corev1.Pod) []corev1.Pod {
	var out []corev1.Pod
	for _, p := range pods {
		if _, exists := p.Annotations[PodTerminationInProgressAnnotationKey]; !exists {
			if p.DeletionTimestamp == nil {
				out = append(out, p)
			}
		}
	}
	return out
}

func GetPodCount(runningPods *map[string][]corev1.Pod) int32 {
	count := 0

	for _, pods := range *runningPods {
		count += len(pods)
	}

	return int32(count)
}

func GetJobCount(pendingJobs *map[string][]PendingJob) int {
	count := 0

	for _, pjs := range *pendingJobs {
		count += len(pjs)
	}

	return count
}

// GetSortedStringificationOfCapabilitiesMap returns a stringified map, of the form <key1>=<value1>;<key2>=<value2>;...
func GetSortedStringificationOfCapabilitiesMap(m *map[string]string) string {
	keys := make([]string, 0, len(*m))
	for key := range *m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	var pairs []string
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, (*m)[key]))
	}

	return strings.Join(pairs, ";")
}

// GetCapabilitiesMapFromString does the inverse of GetSortedStringificationOfCapabilitiesMap
func GetCapabilitiesMapFromString(capabilities string) *map[string]string {
	m := map[string]string{}

	capabilitiesList := strings.Split(capabilities, ";")
	for _, capabilityStr := range capabilitiesList {
		// Note: strings.Cut() slices around the first(!) instance of sep, returning the text before and after sep
		// We need this because an arg might look like this:
		// ExtraAgentContainers=name=c,image=some-image:latest,cpu=500m,memory=2Gi
		key, value, separatorWasFound := strings.Cut(capabilityStr, "=")
		if separatorWasFound {
			m[key] = value
		}
	}

	return &m
}

func GenerateRandomString() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	const length = 5
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[rand.Intn(len(charset))]
	}
	return string(result)
}

func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// Note: Min() and Max() can be removed once we transitioned to Go v1.21 or newer

func Min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func containsString(slice []string, x string) bool {
	for _, n := range slice {
		if x == n {
			return true
		}
	}
	return false
}

func GetMatchingCacheVolume(volumeName string, reusableVolumes []apscalerv1.ReusableCacheVolume) *apscalerv1.ReusableCacheVolume {
	for _, reusableVolume := range reusableVolumes {
		if reusableVolume.Name == volumeName {
			return &reusableVolume
		}
	}
	return nil
}

func GetUnpromisedPvc(pvcs []corev1.PersistentVolumeClaim, volumeName string) *corev1.PersistentVolumeClaim {
	for _, pvc := range pvcs {
		if vn := pvc.Annotations[ReusableCacheVolumeNameAnnotationKey]; vn == volumeName {
			if _, exists := pvc.Annotations[ReusableCacheVolumePromisedAnnotationKey]; !exists {
				return &pvc
			}
		}
	}
	return nil
}

// ComputePvcMaxCountsPerReusableCacheVolume computes a dict that maps from the
// name of a reusable cache volume to the maximum number of PVC instances we want
// to instantiate. It only contains entries for those reusable cache volumes that
// are actually used by at least one Pod. The goal is to avoid creating too many
// PVCs, resulting from the problem that the removal of the "promised" label of
// PVCs is slow.
func ComputePvcMaxCountsPerReusableCacheVolume(agent *apscalerv1.AutoScaledAgent) *map[string]int {
	pvcMaxCounts := map[string]int{}

	for _, podsWithCapabilities := range agent.Spec.PodsWithCapabilities {
		reusableCacheVolumesUsedByThisPod := map[string]bool{} // use map to implement set
		for _, container := range podsWithCapabilities.PodTemplateSpec.Spec.Containers {
			for _, volumeMount := range container.VolumeMounts {
				if matchingCacheVolume := GetMatchingCacheVolume(volumeMount.Name, agent.Spec.ReusableCacheVolumes); matchingCacheVolume != nil {
					reusableCacheVolumesUsedByThisPod[matchingCacheVolume.Name] = true
				}
			}
		}

		for usedReusableCacheVolumeName, _ := range reusableCacheVolumesUsedByThisPod {
			if count, ok := pvcMaxCounts[usedReusableCacheVolumeName]; ok {
				pvcMaxCounts[usedReusableCacheVolumeName] = count + int(*podsWithCapabilities.MaxCount)
			} else {
				pvcMaxCounts[usedReusableCacheVolumeName] = int(*podsWithCapabilities.MaxCount)
			}
		}
	}

	return &pvcMaxCounts
}

func IsPvcLimitExceeded(agent *apscalerv1.AutoScaledAgent, cacheVolumeName string, pvcs []corev1.PersistentVolumeClaim) bool {
	maxPvcCounts := ComputePvcMaxCountsPerReusableCacheVolume(agent)

	// Find the matching, already-existing PVCs (all of which are already promised) and count them
	matchingPvcCount := 0
	for _, pvc := range pvcs {
		if pvc.Annotations[ReusableCacheVolumeNameAnnotationKey] == cacheVolumeName {
			matchingPvcCount += 1
		}
	}

	return matchingPvcCount >= (*maxPvcCounts)[cacheVolumeName]
}

var disappearedPods = make(map[string]time.Time)

// HasPodPermanentlyDisappeared is given the name of a pod that can no longer be found, and returns true if this
// method has been called for the same podName for several seconds, thus increasing the likelihood that the pod is
// really gone (due to client-side caching)
func HasPodPermanentlyDisappeared(podName string) bool {
	if _, exists := disappearedPods[podName]; !exists {
		disappearedPods[podName] = time.Now()
	}

	age := time.Now().Sub(disappearedPods[podName])

	result := age > 5*time.Second

	// Perform garbage collection to avoid memory leaks
	for pN, t := range disappearedPods {
		age := time.Now().Sub(t)
		if age > 60*time.Second {
			delete(disappearedPods, pN)
		}
	}

	return result
}

// ParseExtraAgentContainerDefinition takes a string such as
// "name=ubuntu,image=docker.io/library/ubuntu:22.04,cpu=250m,memory=64Mi||name=postgresql,image=docker.io/library/postgres:14,cpu=250m,memory=1Gi"
// (where the "cpu" and "memory" attributes are optional) and converts them to an
// array of Container objects
func ParseExtraAgentContainerDefinition(extraAgentContainers string) ([]corev1.Container, error) {
	var extraAgentContainerDefinitions []corev1.Container

	definitionStrings := strings.Split(extraAgentContainers, "||")
	for _, definitionString := range definitionStrings {
		attributes := strings.Split(definitionString, ",")
		var name, image, cpu, memory string
		for _, attribute := range attributes {
			keyValues := strings.Split(attribute, "=")
			if len(keyValues) != 2 {
				return nil, fmt.Errorf("malformed extra agent containers attribute: %s", attribute)
			}
			if keyValues[0] == "name" {
				name = keyValues[1]
			} else if keyValues[0] == "image" {
				image = keyValues[1]
			} else if keyValues[0] == "cpu" {
				cpu = keyValues[1]
			} else if keyValues[0] == "memory" {
				memory = keyValues[1]
			}
		}

		if name == "" || image == "" {
			return nil, fmt.Errorf("malformed extra agent containers definition: %s", definitionString)
		}

		container := corev1.Container{
			Name:    name,
			Image:   image,
			Command: []string{"/bin/sh"},
			Args:    []string{"-c", "trap : TERM INT; sleep 9999999999d & wait"},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      AzureWorkingDirMountName,
				MountPath: AzureWorkingDirMountPath,
			}},
			Lifecycle: &corev1.Lifecycle{
				PreStop: &corev1.LifecycleHandler{
					Exec: &corev1.ExecAction{
						Command: []string{"/bin/sh", "-c", "while [ $(pgrep -l Agent.Worker | wc -l) -ne 0 ]; do sleep 1; done"},
					},
				},
			},
			ImagePullPolicy: corev1.PullAlways,
		}

		if cpu != "" {
			cpuQuantity, err := resource.ParseQuantity(cpu)
			if err != nil {
				return nil, err
			}

			container.Resources.Requests = map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: cpuQuantity}
			container.Resources.Limits = map[corev1.ResourceName]resource.Quantity{corev1.ResourceCPU: cpuQuantity}
		}

		if memory != "" {
			memoryQuantity, err := resource.ParseQuantity(memory)
			if err != nil {
				return nil, err
			}

			if container.Resources.Requests == nil {
				container.Resources.Requests = make(map[corev1.ResourceName]resource.Quantity)
				container.Resources.Limits = make(map[corev1.ResourceName]resource.Quantity)
			}
			container.Resources.Requests[corev1.ResourceMemory] = memoryQuantity
			container.Resources.Limits[corev1.ResourceMemory] = memoryQuantity
		}

		extraAgentContainerDefinitions = append(extraAgentContainerDefinitions, container)
	}

	return extraAgentContainerDefinitions, nil
}

func GetPoolIdFromName(ctx context.Context, azurePat string, httpClient *http.Client,
	spec *apscalerv1.AutoScaledAgentSpec) (int64, error) {
	cacheKey := spec.OrganizationUrl + spec.PoolName
	if cachedPoolName, ok := cachedAzpPoolIds[cacheKey]; ok {
		return cachedPoolName, nil
	}

	url := fmt.Sprintf("%s/_apis/distributedtask/pools?poolName=%s", spec.OrganizationUrl, spec.PoolName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.SetBasicAuth("", azurePat)
	if err != nil {
		return 0, err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		if IsVerboseDebugLoggingEnabled() {
			fmt.Printf("Response: %s\n", string(bytes))
		}
		return 0, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	var result AzurePipelinesApiPoolNameResponse
	err = json.Unmarshal(bytes, &result)
	if err != nil {
		return 0, err
	}

	count := len(result.Value)
	if count == 0 {
		return 0, fmt.Errorf("agent pool with name `%s` not found in response", spec.PoolName)
	}

	if count != 1 {
		return 0, fmt.Errorf("found %d agent pools with name `%s`", count, spec.PoolName)
	}

	poolId := int64(result.Value[0].ID)

	cachedAzpPoolIds[cacheKey] = poolId

	return poolId, nil
}

func IsVerboseDebugLoggingEnabled() bool {
	debugFilePath := os.Getenv(DebugLogEnvVarName)
	if _, err := os.Stat(debugFilePath); err == nil {
		return true
	}
	return false
}
