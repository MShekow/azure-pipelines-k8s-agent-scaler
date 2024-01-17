package fake_platform_server

import (
	"encoding/json"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	"github.com/gorilla/mux"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)
import "net/http"

const DefaultPort = 8181
const AgentsContainerUrl = "/_apis/distributedtask/pools/{pool-id:[0-9]+}/agents"
const ListPoolsUrl = "/_apis/distributedtask/pools"
const JobsListUrl = "/_apis/distributedtask/pools/{pool-id:[0-9]+}/jobrequests"
const SpecificAgentUrl = "/_apis/distributedtask/pools/{pool-id:[0-9]+}/agents/{agent-id:[0-9]+}"
const AssignJobUrl = "/fake-api/assign-job/{job-id:[0-9]+}"
const FinishJobUrl = "/fake-api/finish-job/{job-id:[0-9]+}"

type FakeAzurePipelinesPlatformServer struct {
	Jobs      []Job
	Agents    []Agent
	Requests  []Request
	PoolId    int
	PoolName  string
	server    *http.Server
	isStopped bool
}

func NewFakeAzurePipelinesPlatformServer() *FakeAzurePipelinesPlatformServer {
	router := mux.NewRouter()

	platformServer := &FakeAzurePipelinesPlatformServer{
		Jobs:     []Job{},
		Agents:   []Agent{},
		Requests: []Request{},
		PoolId:   0,
		PoolName: "",
		server: &http.Server{
			Addr:    fmt.Sprintf(":%d", DefaultPort),
			Handler: router,
		},
	}

	router.HandleFunc(AssignJobUrl, platformServer.assignJob).Methods("POST")
	router.HandleFunc(FinishJobUrl, platformServer.finishJob).Methods("POST")
	router.HandleFunc(AgentsContainerUrl, platformServer.addAgent).Methods("POST")
	router.HandleFunc(AgentsContainerUrl, platformServer.listAgents).Methods("GET")
	router.HandleFunc(SpecificAgentUrl, platformServer.deleteAgent).Methods("DELETE")
	router.HandleFunc(JobsListUrl, platformServer.listJobs).Methods("GET")
	router.HandleFunc(ListPoolsUrl, platformServer.listPools).Methods("GET")

	return platformServer
}

// Start starts the fake platform server on the given port, using DefaultPort by default. The server runs in the
// background, meaning that Start does not block for long (only half a second to check for port-binding errors).
func (f *FakeAzurePipelinesPlatformServer) Start(port int) error {
	if f.isStopped {
		return fmt.Errorf("cannot start server after it has been stopped")
	}

	if port == 0 {
		port = DefaultPort
	}

	earlyExit := make(chan error)

	go func() {
		if err := f.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("listening on port %d failed: %s\n", port, err)
			earlyExit <- err
		}
	}()

	// Wait for the Go routine to return an error for half a second, then return nil because we assume that the server
	// has started successfully.
	for i := 0; i < 5; i++ {
		select {
		case err := <-earlyExit:
			return err
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	return nil
}

// Stop stops the fake platform server. After stopping it, it cannot be started again.
func (f *FakeAzurePipelinesPlatformServer) Stop() error {
	f.isStopped = true
	return f.server.Close()
}

// CreatePool initializes the pool with the given poolId and poolName. It is called from the test code.
func (f *FakeAzurePipelinesPlatformServer) CreatePool(poolId int, poolName string) {
	f.PoolId = poolId
	f.PoolName = poolName
}

// getJob returns the job with the given jobId, or nil if no such job exists.
func (f *FakeAzurePipelinesPlatformServer) getJob(jobId int) *Job {
	for _, job := range f.Jobs {
		if job.ID == jobId {
			return &job
		}
	}
	return nil
}

// AddJob advertises a new job. It is called from the test code.
func (f *FakeAzurePipelinesPlatformServer) AddJob(jobId int, poolId int, duration int64, demands map[string]string) error {
	// Verify that the jobId is globally unique
	if f.getJob(jobId) != nil {
		return fmt.Errorf("JobId already in use")
	}

	if f.PoolId != poolId {
		return fmt.Errorf("PoolId does not match")
	}

	demandsArray := []string{}
	for k, v := range demands {
		demandsArray = append(demandsArray, k+" -equals "+v)
	}
	// Sort the demands array alphabetically to improve test stability
	sort.Slice(demandsArray, func(i, j int) bool {
		return demandsArray[i] < demandsArray[j]
	})

	job := Job{
		ID:       jobId,
		PoolID:   poolId,
		State:    Pending,
		Duration: duration,
		Demands:  demandsArray,
	}
	f.saveJob(job)

	return nil
}

// CancelJob cancels the job with the given jobId, by deleting it from the list of jobs. It is called from the test code.
func (f *FakeAzurePipelinesPlatformServer) CancelJob(jobId int) error {
	for i, job := range f.Jobs {
		if job.ID == jobId {
			f.Jobs = append(f.Jobs[:i], f.Jobs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("JobId not found")
}

// Reset resets the fake platform server. It is called from the test code.
func (f *FakeAzurePipelinesPlatformServer) Reset() {
	f.Jobs = []Job{}
	f.Agents = []Agent{}
	f.Requests = []Request{}
	f.PoolId = 0
	f.PoolName = ""
}

// saveJob saves the given job to the fake platform server.
func (f *FakeAzurePipelinesPlatformServer) saveJob(job Job) {
	foundJob := false
	for i, j := range f.Jobs {
		if job.ID == j.ID {
			// Update the existing job
			f.Jobs[i] = job
			foundJob = true
			break
		}
	}
	if !foundJob {
		f.Jobs = append(f.Jobs, job)
	}
}

func (f *FakeAzurePipelinesPlatformServer) verifyPoolId(w http.ResponseWriter, r *http.Request) (int, error) {
	vars := mux.Vars(r)
	poolId, err := strconv.Atoi(vars["pool-id"])
	if err != nil {
		// Return a 400 Bad Request error with a JSON body
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf(`{"error": "invalid pool id format, must be a valid integer: %s"}`,
			vars["pool-id"])))
		return 0, err
	}

	if f.PoolId != poolId {
		// Return a 404 Not Found error with a JSON body
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf(`{"error": "pool with id %d not found"}`, poolId)))
		return 0, fmt.Errorf("pool with id %d not found", poolId)
	}

	return poolId, nil
}

// assignJob handles the "assign-job" request from our fake Azure Pipelines agent.
func (f *FakeAzurePipelinesPlatformServer) assignJob(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobId, err := strconv.Atoi(vars["job-id"])
	if err != nil {
		// Return a 400 Bad Request error with a JSON body
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf(`{"error": "invalid job id format, must be a valid integer: %s"}`,
			vars["job-id"])))
		return
	}
	job := f.getJob(jobId)

	if job == nil {
		// Return a 404 Not Found error with a JSON body
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf(`{"error": "job with id %d not found"}`, jobId)))
		return
	}

	if job.State != Pending {
		// Return a 409 Conflict error with a JSON body
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(fmt.Sprintf(`{"error": "job with id %d is not in state Pending"}`, jobId)))
		return
	}

	// Get agent name and capabilities from the request headers
	agentName := r.Header.Get("X-AZP-Agent-Name")
	agentCapabilitiesStr := r.Header.Get("X-AZP-AGENT-CAPABILITIES")
	agentCapabilities := strings.Split(agentCapabilitiesStr, ";")

	// Verify that the agent exists
	agentExists := false
	for _, agent := range f.Agents {
		if agent.Name == agentName {
			agentExists = true
			break
		}
	}
	if !agentExists {
		// Return a 404 Not Found error with a JSON body
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf(`{"error": "agent with name %s not found"}`, agentName)))
		return
	}

	job.State = InProgress
	f.saveJob(*job)

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:              AssignJob,
		AgentName:         agentName,
		AgentCapabilities: agentCapabilities,
		PoolID:            job.PoolID,
		JobID:             jobId,
	})

	// Return a 200 OK response
	w.WriteHeader(http.StatusOK)
}

// finishJob handles the "finish-job" request from our fake Azure Pipelines agent.
func (f *FakeAzurePipelinesPlatformServer) finishJob(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobId, err := strconv.Atoi(vars["job-id"])
	if err != nil {
		// Return a 400 Bad Request error with a JSON body
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf(`{"error": "invalid job id format, must be a valid integer: %s"}`,
			vars["job-id"])))
		return
	}
	job := f.getJob(jobId)

	if job == nil {
		// Return a 404 Not Found error with a JSON body
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(fmt.Sprintf(`{"error": "job with id %d not found"}`, jobId)))
		return
	}

	job.State = Finished
	f.saveJob(*job)

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:   FinishJob,
		PoolID: job.PoolID,
		JobID:  jobId,
	})

	// Return a 200 OK response
	w.WriteHeader(http.StatusOK)
}

// addAgent handles the "add-agent" request from our fake Azure Pipelines agent. It expects the poolID in the URL, and
// the agent name and capabilities in the request headers (X-AZP-Agent-Name and X-AZP-AGENT-CAPABILITIES).
// In case of success, it returns a 200 OK response and returns the auto-generated agent ID in the response body (JSON).
func (f *FakeAzurePipelinesPlatformServer) addAgent(w http.ResponseWriter, r *http.Request) {
	poolId, err := f.verifyPoolId(w, r)
	if err != nil {
		return
	}

	// Get agent name and capabilities from the request headers
	agentName := r.Header.Get("X-AZP-Agent-Name")
	agentCapabilitiesStr := r.Header.Get("X-AZP-AGENT-CAPABILITIES")
	agentCapabilities := strings.Split(agentCapabilitiesStr, ";")

	// Verify that the agent does not exist yet
	for _, agent := range f.Agents {
		if agent.Name == agentName {
			// Return a 409 Conflict error with a JSON body
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(fmt.Sprintf(`{"error": "agent with name %s already exists", "id": %d}`,
				agentName, agent.ID)))

			// Add the request to the list of requests
			f.Requests = append(f.Requests, Request{
				Type:              ReplaceAgent,
				AgentName:         agentName,
				AgentCapabilities: agentCapabilities,
				PoolID:            poolId,
			})

			return
		}
	}

	// Add the agent to the list of agents
	agent := Agent{
		Name: agentName,
		ID:   len(f.Agents) + 1,
	}
	f.Agents = append(f.Agents, agent)

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:              CreateAgent,
		AgentName:         agentName,
		AgentCapabilities: agentCapabilities,
		PoolID:            poolId,
	})

	// Return a 200 OK response with the agent ID in the response body
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`{"id": %d}`, agent.ID)))
}

func (f *FakeAzurePipelinesPlatformServer) listAgents(w http.ResponseWriter, r *http.Request) {
	poolId, err := f.verifyPoolId(w, r)
	if err != nil {
		return
	}

	var agents []service.AzurePipelinesAgent
	for _, agent := range f.Agents {
		agents = append(agents, service.AzurePipelinesAgent{
			CreatedOn: time.Time{},
			Name:      agent.Name,
			Id:        agent.ID,
			Status:    "offline",
		})
	}

	apiResponse := service.AzurePipelinesAgentList{
		Count: len(agents),
		Value: agents,
	}

	bytesResponse, err := json.Marshal(apiResponse)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"error": "failed to marshal response: %s"}`, err.Error())))
		return
	}

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:   ListAgent,
		PoolID: poolId,
	})

	w.WriteHeader(http.StatusOK)
	w.Write(bytesResponse)
}
func (f *FakeAzurePipelinesPlatformServer) deleteAgent(w http.ResponseWriter, r *http.Request) {
	poolId, err := f.verifyPoolId(w, r)
	if err != nil {
		return
	}

	vars := mux.Vars(r)
	agentId, err := strconv.Atoi(vars["agent-id"])
	if err != nil {
		// Return a 400 Bad Request error with a JSON body
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(fmt.Sprintf(`{"error": "invalid agent id format, must be a valid integer: %s"}`,
			vars["agent-id"])))
		return
	}

	for i, agent := range f.Agents {
		if agent.ID == agentId {
			f.Agents = append(f.Agents[:i], f.Agents[i+1:]...)
			break
		}
	}

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:   DeleteAgent,
		PoolID: poolId,
	})

	w.WriteHeader(http.StatusOK)
}

// listJobs handles the "list-jobs" request from our fake Azure Pipelines agent. It expects the pool ID in the URL.
// The response is formatted using the service.AzurePipelinesApiJobRequests structure. There is some information only
// needed by the fake agent, which the service.AzurePipelinesApiJobRequests does not contain, thus we use some of its
// fields for our own purposes:
// - ScopeID: contains the duration of the job (in nanoseconds)
// - HostID: contains the StartDelay (in nanoseconds)
// - PlanID: contains the FinishDelay (in nanoseconds)
func (f *FakeAzurePipelinesPlatformServer) listJobs(w http.ResponseWriter, r *http.Request) {
	poolId, err := f.verifyPoolId(w, r)
	if err != nil {
		return
	}

	var jobRequests []service.AzurePipelinesApiJobRequest
	for _, job := range f.Jobs {
		if job.State == Pending || job.State == InProgress {
			assignTime := time.Time{}
			if job.State == InProgress {
				assignTime = time.Now()
			}
			jobRequests = append(jobRequests, service.AzurePipelinesApiJobRequest{
				RequestID:  job.ID,
				JobID:      fmt.Sprintf("%d", job.ID),
				AssignTime: assignTime,
				ScopeID:    fmt.Sprintf("%d", job.Duration),
				HostID:     fmt.Sprintf("%d", job.StartDelay),
				PlanID:     fmt.Sprintf("%d", job.FinishDelay),
				Demands:    job.Demands,
				PoolID:     job.PoolID,
			})
		}
	}

	apiResponse := service.AzurePipelinesApiJobRequests{
		Count: len(jobRequests),
		Value: jobRequests,
	}

	bytesResponse, err := json.Marshal(apiResponse)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"error": "failed to marshal response: %s"}`, err.Error())))
		return
	}

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type:   ListJob,
		PoolID: poolId,
	})

	w.WriteHeader(http.StatusOK)
	w.Write(bytesResponse)
}

// listPools handles the "list-pools" request from our fake Azure Pipelines agent. It expects the poolName in the URL.
// It returns either an empty list (`[]`) or a list with a single pool (`[{"id": <pool-id>}]`) in case the provided
// pool name matches the pool name of the fake platform server.
func (f *FakeAzurePipelinesPlatformServer) listPools(w http.ResponseWriter, r *http.Request) {
	poolName := strings.TrimSpace(r.URL.Query().Get("poolName")) // is an empty string if it is missing in the URL
	if poolName == "" {
		// Return a 400 Bad Request error with a JSON body
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "query parameter poolName is missing"}`))
		return
	}

	apiResponse := service.AzurePipelinesApiPoolNameResponse{
		Value: []struct {
			ID int `json:"id"`
		}{},
	}

	if f.PoolName == poolName {
		apiResponse.Value = append(apiResponse.Value, struct {
			ID int `json:"id"`
		}{ID: f.PoolId})
	}

	bytesResponse, err := json.Marshal(apiResponse)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"error": "failed to marshal response: %s"}`, err.Error())))
		return
	}

	// Add the request to the list of requests
	f.Requests = append(f.Requests, Request{
		Type: GetPoolId,
	})

	w.WriteHeader(http.StatusOK)
	w.Write(bytesResponse)
}
