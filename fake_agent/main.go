package main

import (
	"errors"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_agent/utils"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	"net/http"
	"os"
	"time"
)

/*
This is a simple fake Azure Pipelines Agent that connects to the fake platform server and runs fake jobs, then
terminates again.

It expects the environment variables AZP_AGENT_NAME, AZP_URL, and AZP_POOL to be filled (AZP_TOKEN is not used).
Also, the environment variable WORKER_BINARY must be set to the path of the "Agent.Worker" binary
(see directory "fake_agent_worker_process").
*/

func main() {
	httpClient := &http.Client{
		Timeout: fake_agent_utils.HttpRequestTimeout,
	}

	// Get pool name and organization URL from environment variables
	organizationUrl := os.Getenv("AZP_URL")
	poolName := os.Getenv("AZP_POOL")
	agentName := os.Getenv("AZP_AGENT_NAME")

	poolId, err := fake_agent_utils.GetPoolIdFromName(organizationUrl, poolName, httpClient)
	if err != nil {
		fmt.Printf("Unable to retrieve pool ID from name: %v\n", err)
		os.Exit(1)
	}

	agentId, err := fake_agent_utils.RegisterAsAgent(organizationUrl, poolId, agentName, httpClient)
	if err != nil {
		fmt.Printf("Unable to register as agent: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Registered as agent named %s with ID %d\n", agentName, agentId)

	err = fake_agent_utils.CopyWorkerProcessBinary()
	if err != nil {
		fmt.Printf("Unable to copy worker process binary: %v\n", err)
		os.Exit(1)
	}

	assignedJob := fake_platform_server.Job{}
	hasAssignedJob := false
	assignedTimestamp := time.Time{}
	startedTimestamp := time.Time{}
	stoppedTimestamp := time.Time{}
	proc := &os.Process{}

	// Main loop
	/*
		The following happens in the loop
		- If we did not assign a job yet:
			- Try to assign the agent to a job with matching demands, gracefully handle conflict errors
			- If assignment was successful, set hasAssignedJob to true and assignedJob to the assigned job
				- If assignedJob.StartDelay is > 0, set assignedTimestamp to <now>
				- Else: set both assignedTimestamp and startedTimestamp to <now>
		- Else (job has been assigned):
			- If the job has been cancelled (no longer present in the list):
				- If startedTimestamp is set but stoppedTimestamp is not set: kill the fake "Agent.Worker" binary
				- break out of the loop
			- If startedTimestamp is not set yet:
				- If <now> >= assignedTimestamp + assignedJob.StartDelay: set startedTimestamp to <now> and call the fake "Agent.Worker" binary
			- Else:
				- If <now> >= startedTimestamp + assignedJob.Duration: kill the fake "Agent.Worker" binary, set stoppedTimestamp to <now>,
				  call the finish-job API and break out of the loop
		- Sleep for 1 second
	*/
	for {
		fmt.Println("Checking for pending jobs")
		jobs, err := fake_agent_utils.GetPendingJobs(organizationUrl, poolId, httpClient)
		if err != nil {
			fmt.Printf("Unable to retrieve pending jobs: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Retrieved %d pending/running jobs\n", len(jobs))

		if !hasAssignedJob {
			// Try to assign the agent to a pending job, given that capabilities and demands match
			for _, job := range jobs {
				if job.State == fake_platform_server.Pending && fake_agent_utils.DoesFakeAgentCapabilitiesSatisfyDemands(job.Demands) {
					err = fake_agent_utils.AssignJob(organizationUrl, agentName, job.ID, httpClient)
					if err != nil {
						var agentAssignmentConflictError *fake_agent_utils.ConflictError
						if errors.As(err, &agentAssignmentConflictError) {
							continue
						}
						fmt.Printf("Unable to assign job: %v\n", err)
						os.Exit(1)
					}
					assignedJob = job
					hasAssignedJob = true
					break
				} else {
					fmt.Printf("Job with ID %d does not match capabilities or demands. State=%s, demands=%v\n", job.ID, job.State, job.Demands)
				}
			}

			if hasAssignedJob {
				fmt.Printf("Assigned job with ID %d, Duration=%d, StartDelay=%d, FinishDelay=%d\n",
					assignedJob.ID, assignedJob.Duration, assignedJob.StartDelay, assignedJob.FinishDelay)
				if assignedJob.StartDelay > 0 {
					assignedTimestamp = time.Now()
				} else {
					assignedTimestamp = time.Now()
					startedTimestamp = time.Now()

					// Start the "Agent.Worker" process
					proc, err = fake_agent_utils.StartWorkerProcess()
					if err != nil {
						fmt.Printf("Unable to start worker process: %v\n", err)
						os.Exit(1)
					}
					fmt.Println("Started worker process")
				}
			}

		} else {
			// If the job has been cancelled (no longer present in the list):
			// - If startedTimestamp is set but stoppedTimestamp is not set: kill the fake "Agent.Worker" binary
			// - break out of the loop
			jobIsCancelled := true
			for _, job := range jobs {
				if job.ID == assignedJob.ID {
					jobIsCancelled = false
					break
				}
			}

			if jobIsCancelled {
				if !stoppedTimestamp.IsZero() {
					err = proc.Kill()
					if err != nil {
						return
					}
				}
				fmt.Println("Job has been cancelled")
				break
			}

			if startedTimestamp.IsZero() {
				if time.Now().After(assignedTimestamp.Add(time.Duration(assignedJob.StartDelay))) {
					startedTimestamp = time.Now()

					// Start the "Agent.Worker" process
					proc, err = fake_agent_utils.StartWorkerProcess()
					if err != nil {
						fmt.Printf("Unable to start worker process: %v\n", err)
						os.Exit(1)
					}
					fmt.Println("StartDelay has passed, starting worker process")
				}
			} else {
				if time.Now().After(startedTimestamp.Add(time.Duration(assignedJob.Duration))) {
					// Kill the "Agent.Worker" process
					err = proc.Kill()
					if err != nil {
						return
					}

					stoppedTimestamp = time.Now()

					// Call the finish-job API
					err = fake_agent_utils.FinishJob(organizationUrl, agentName, assignedJob.ID, httpClient)
					if err != nil {
						fmt.Printf("Unable to finish job: %v\n", err)
						os.Exit(1)
					}

					fmt.Println("Duration has passed, stopped worker process")

					break
				}
			}
		}

		time.Sleep(1 * time.Second)
	}

	// Finally, kill the "Agent.Worker" process again, and once FinishDelay has passed, deregister the agent again
	if !startedTimestamp.IsZero() {
		err = proc.Kill()
		if err != nil {
			return
		}
	}

	time.Sleep(time.Duration(assignedJob.FinishDelay))

	err = fake_agent_utils.DeleteAgent(organizationUrl, agentId, poolId, httpClient)
	if err != nil {
		fmt.Printf("Unable to delete agent: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Successfully deleted agent, exiting")
}
