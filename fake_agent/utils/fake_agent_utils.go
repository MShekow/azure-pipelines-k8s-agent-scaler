package fake_agent_utils

import (
	"encoding/json"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ConflictError struct {
}

func (e *ConflictError) Error() string {
	return "conflict error"
}

const HttpRequestTimeout = 10 * time.Second

func GetPoolIdFromName(organizationUrl, poolName string, httpClient *http.Client) (int64, error) {
	url := fmt.Sprintf("%s/_apis/distributedtask/pools?poolName=%s", organizationUrl, poolName)

	response, err := httpClient.Get(url)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return 0, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	var result service.AzurePipelinesApiPoolNameResponse
	err = json.Unmarshal(bytes, &result)
	if err != nil {
		return 0, err
	}

	count := len(result.Value)

	if count != 1 {
		return 0, fmt.Errorf("found %d agent pools with name `%s`", count, poolName)
	}

	poolId := int64(result.Value[0].ID)
	return poolId, nil
}

type AddAgentResponse struct {
	Id int `json:"id"`
}

func RegisterAsAgent(organizationUrl string, poolId int64, agentName string, httpClient *http.Client) (int, error) {
	url := organizationUrl +
		strings.Replace(fake_platform_server.AgentsContainerUrl, "{pool-id:[0-9]+}", fmt.Sprintf("%d", poolId), 1)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-AZP-Agent-Name", agentName)
	req.Header.Set("X-AZP-AGENT-CAPABILITIES", strings.Join(os.Environ(), ";"))

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
		if response.StatusCode != 409 {
			// the 409 status code is expected if the agent already exists, the id is returned in the response body
			return 0, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
		}
	}

	// Extract the agent ID from the response body, which has the form {"id": 1}
	var result AddAgentResponse
	err = json.Unmarshal(bytes, &result)
	if err != nil {
		return 0, err
	}

	return result.Id, nil
}

func CopyWorkerProcessBinary() error {
	// Copy /bin/sleep to "Agent.Worker"
	workerProcessBinary := os.Getenv("WORKER_BINARY")
	err := CopyFileIfNotExists(workerProcessBinary, "Agent.Worker")
	if err != nil {
		return err
	}
	return nil
}

func CopyFileIfNotExists(absoluteSource, relativeDest string) error {
	// Check if destination file exists already
	if _, err := os.Stat(relativeDest); err == nil {
		return nil
	}

	// Open source file for reading
	sourceFile, err := os.Open(absoluteSource)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Create destination file
	destFile, err := os.Create(filepath.Join(".", relativeDest))
	if err != nil {
		return err
	}
	defer destFile.Close()

	// Copy contents from source to destination
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	// Sync to ensure all data is written to disk
	err = destFile.Sync()
	if err != nil {
		return err
	}

	// Get the source file info (including permissions)
	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		return err
	}

	// Copy the source file permissions to the destination file
	err = os.Chmod(relativeDest, sourceInfo.Mode())
	if err != nil {
		return err
	}

	return nil
}

// GetPendingJobs returns all pending or in-progress jobs for the given pool ID
func GetPendingJobs(organizationUrl string, poolId int64, httpClient *http.Client) ([]fake_platform_server.Job, error) {
	url := organizationUrl +
		strings.Replace(fake_platform_server.JobsListUrl, "{pool-id:[0-9]+}", fmt.Sprintf("%d", poolId), 1)

	response, err := httpClient.Get(url)
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

	var jobRequestsFromApi service.AzurePipelinesApiJobRequests
	err = json.Unmarshal(b, &jobRequestsFromApi)
	if err != nil {
		return nil, err
	}

	// Convert jobRequestsFromApi to fake_platform_server.Job
	var jobs []fake_platform_server.Job
	for _, jobRequestFromApi := range jobRequestsFromApi.Value {
		jobId, _ := strconv.Atoi(jobRequestFromApi.JobID)
		durationInNanos, _ := strconv.ParseInt(jobRequestFromApi.ScopeID, 10, 64)
		startDelayInNanos, _ := strconv.ParseInt(jobRequestFromApi.HostID, 10, 64)
		finishDelayInNanos, _ := strconv.ParseInt(jobRequestFromApi.PlanID, 10, 64)
		job := fake_platform_server.Job{
			ID:          jobId,
			PoolID:      int(poolId),
			State:       fake_platform_server.Pending,
			Duration:    durationInNanos,
			StartDelay:  startDelayInNanos,
			FinishDelay: finishDelayInNanos,
			Demands:     jobRequestFromApi.Demands,
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func AssignJob(organizationUrl string, agentName string, jobId int, httpClient *http.Client) error {
	url := organizationUrl +
		strings.Replace(fake_platform_server.AssignJobUrl, "{job-id:[0-9]+}", fmt.Sprintf("%d", jobId), 1)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("X-AZP-Agent-Name", agentName)
	req.Header.Set("X-AZP-AGENT-CAPABILITIES", strings.Join(os.Environ(), ";"))

	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		if response.StatusCode == 409 {
			return &ConflictError{}
		}
		return fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	return nil
}

func FinishJob(organizationUrl string, jobId int, httpClient *http.Client) error {
	url := organizationUrl +
		strings.Replace(fake_platform_server.FinishJobUrl, "{job-id:[0-9]+}", fmt.Sprintf("%d", jobId), 1)

	response, err := httpClient.Post(url, "application/json", nil)
	if err != nil {
		return err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	return nil
}

func DeleteAgent(organizationUrl string, agentId int, poolId int64, httpClient *http.Client) error {
	replacedPoolIdUrl := strings.Replace(fake_platform_server.SpecificAgentUrl, "{pool-id:[0-9]+}", fmt.Sprintf("%d", poolId), 1)
	replacedAgentIdUrl := strings.Replace(replacedPoolIdUrl, "{agent-id:[0-9]+}", fmt.Sprintf("%d", agentId), 1)
	url := organizationUrl + replacedAgentIdUrl

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}

	if !(response.StatusCode >= 200 && response.StatusCode <= 299) {
		return fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	return nil
}

func DoesFakeAgentCapabilitiesSatisfyDemands(demands []string) bool {
	capabilitiesMapFromEnv := map[string]string{}
	for _, env := range os.Environ() {
		envParts := strings.Split(env, "=")
		if len(envParts) == 2 {
			capabilitiesMapFromEnv[envParts[0]] = envParts[1]
		}
	}

	for _, demand := range demands {
		demandParts := strings.Split(demand, " -equals ")
		if len(demandParts) != 2 {
			continue
		}
		demandKey := demandParts[0]
		demandValue := demandParts[1]
		if capabilitiesMapFromEnv[demandKey] != demandValue {
			return false
		}
	}
	return true
}

func StartWorkerProcess() (*os.Process, error) {
	cmd := exec.Command("./Agent.Worker")
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
