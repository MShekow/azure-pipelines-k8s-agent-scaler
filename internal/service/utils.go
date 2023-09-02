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

var dummyAgentNames []string

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

	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d response: %s", url, response.StatusCode, string(bytes))
	}

	var jobRequestsFromApi AzurePipelinesApiJobRequests
	err = json.Unmarshal(bytes, &jobRequestsFromApi)
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
	spec *apscalerv1.AutoScaledAgentSpec) ([]string, error) {
	if len(dummyAgentNames) > 0 {
		return dummyAgentNames, nil // TODO this needs to be checked individually for each AutoScaledAgent CR, not just globally
	}
	logger := log.FromContext(ctx)

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

		dummyAgentNames = append(dummyAgentNames, dummyAgentName)

		if response.StatusCode == 409 {
			continue // 409 = HTTP "conflict", because an agent with the given name already exists, which is fine (EAFP)
		}

		if response.StatusCode != 200 {
			return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
		}
	}

	logger.Info("Successfully created/updated dummy agents", "dummyAgentNames", dummyAgentNames)

	return dummyAgentNames, nil
}

func getDummyAgentName(capabilities *map[string]string) string {
	if len(*capabilities) == 0 {
		return DummyAgentNamePrefix
	}
	return fmt.Sprintf("%s-%s", DummyAgentNamePrefix, ComputeMapHash(capabilities))
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

func GetPodCount(runningPods *map[string][]corev1.Pod) int32 {
	count := 0

	for _, pods := range *runningPods {
		count += len(pods)
	}

	return int32(count)
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
