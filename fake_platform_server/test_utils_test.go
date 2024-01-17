package fake_platform_server_test

import (
	"encoding/json"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	"io"
	"net/http"
	"strings"
)

func ListPools(httpClient *http.Client, serverPort int, poolName string) (*service.AzurePipelinesApiPoolNameResponse, error) {
	url := fmt.Sprintf("http://localhost:%d%s?poolName=%s", serverPort, fake_platform_server.ListPoolsUrl, poolName)

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
		return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	var result service.AzurePipelinesApiPoolNameResponse
	err = json.Unmarshal(b, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func ListAgents(httpClient *http.Client, serverPort int, poolId int) (*service.AzurePipelinesAgentList, error) {
	serviceUrl := strings.Replace(fake_platform_server.AgentsContainerUrl, "{pool-id:[0-9]+}", fmt.Sprintf("%d", poolId), 1)
	url := fmt.Sprintf("http://localhost:%d%s", serverPort, serviceUrl)

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
		return nil, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d", url, response.StatusCode)
	}

	var result service.AzurePipelinesAgentList
	err = json.Unmarshal(b, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}
