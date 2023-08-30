package service

import (
	"context"
	"encoding/json"
	"fmt"
	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"io"
	corev1 "k8s.io/api/core/v1"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"
)

func CreateHTTPClient() *http.Client {
	// default the timeout to 300ms
	timeout := 300 * time.Millisecond
	httpClient := &http.Client{
		Timeout: timeout,
	}
	return httpClient
}

func GetPendingJobs(ctx context.Context, poolId int64, azurePat string, httpClient *http.Client,
	spec *apscalerv1.AutoScaledAgentSpec) (*PendingJobsContainer, error) {

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

	pendingJobs := PendingJobsContainer{}

	for _, jobRequestFromApi := range jobRequestsFromApi.Value {
		if jobRequestFromApi.Result != nil {
			continue // Job has already completed
		}

		pendingJobs.AddJobRequest(&jobRequestFromApi)
	}

	return &pendingJobs, nil
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
