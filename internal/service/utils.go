package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"hash/fnv"
	"io"
	corev1 "k8s.io/api/core/v1"
	"math/rand"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sort"
	"strings"
	"time"
)

var (
	dummyAgentNames            = make(map[string][]string) // maps from AutoScaledAgent.Name to the list of dummy agent names
	lastDeadDummyAgentDeletion = time.Date(1999, 1, 1, 0, 0, 0, 0, time.Local)
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

	pendingJobs := PendingJobsWrapper{}

	for _, jobRequestFromApi := range jobRequestsFromApi.Value {
		if jobRequestFromApi.Result != nil {
			continue // Job has already completed
		}

		pendingJobs.AddJobRequest(&jobRequestFromApi)
	}

	return &pendingJobs, nil
}

// CreateOrUpdateDummyAgents registers one dummy agent for each
// PodsWithCapabilities, as otherwise Azure DevOps would immediately abort a
// pipeline
func CreateOrUpdateDummyAgents(ctx context.Context, poolId int64, azurePat string, httpClient *http.Client,
	crName string, spec *apscalerv1.AutoScaledAgentSpec) ([]string, error) {
	if _, exists := dummyAgentNames[crName]; exists {
		return dummyAgentNames[crName], nil
	}
	logger := log.FromContext(ctx)

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

	return newDummyAgentNames, nil
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
	if time.Now().Add(-time.Minute * 30).Before(lastDeadDummyAgentDeletion) {
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
			cutOffDate := time.Now().Add(-time.Hour * 2)
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
			cutOffDate := time.Now().Add(-time.Hour * 5)
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
		capabilityTuple := strings.Split(capabilityStr, "=")
		if len(capabilityTuple) == 2 {
			m[capabilityTuple[0]] = capabilityTuple[1]
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

func GetAvailablePvc(pvcs []corev1.PersistentVolumeClaim, volumeName string) *corev1.PersistentVolumeClaim {
	for _, pvc := range pvcs {
		if vn := pvc.Annotations[ReusableCacheVolumeNameAnnotationKey]; vn == volumeName {
			if _, exists := pvc.Annotations[ReusableCacheVolumePromisedAnnotationKey]; !exists {
				return &pvc
			}
		}
	}
	return nil
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
