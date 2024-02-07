package service_test

import (
	"context"
	"fmt"
	v1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

const serverPortRangeStart = 8181
const serverPortRangeEnd = 8281
const serverPoolId = 5
const serverPoolName = "test"
const fakePat = ""

func GetFreeLocalPort(portRangeStart int, portRangeEnd int) (int, error) {
	for port := portRangeStart; port <= portRangeEnd; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			err = ln.Close()
			if err != nil {
				return 0, err
			}
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port found in range %d-%d", portRangeStart, portRangeEnd)
}

func durationMustParse(s string) *v1meta.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return &v1meta.Duration{Duration: d}
}

var _ = Describe("Integration tests", func() {
	var httpClient *http.Client
	var server *fake_platform_server.FakeAzurePipelinesPlatformServer

	// Create httpClient in BeforeEach
	BeforeEach(func() {
		httpClient = &http.Client{
			Timeout: 10 * time.Hour,
		}
	})

	When("Running against the real AZP API", func() {
		It("GetPoolIdFromName() should return the expected ID of a prepared pool", func() {
			// Note: all values are stored as secrets in the GitHub repo
			pat := os.Getenv("TEST_AZP_TOKEN")
			organizationUrl := os.Getenv("TEST_AZP_ORGANIZATION_URL")
			poolName := os.Getenv("TEST_AZP_POOL_NAME")
			expectedPoolIdStr := os.Getenv("TEST_EXPECTED_AZP_POOL_ID")

			// Skip the test if any of the variables are empty
			if pat == "" || organizationUrl == "" || poolName == "" || expectedPoolIdStr == "" {
				Skip("Skipping test because one or more of the required environment variables are not set")
			}

			expectedPoolId, err := strconv.ParseInt(expectedPoolIdStr, 10, 64)
			Expect(err).ToNot(HaveOccurred())

			agentSpec := v1.AutoScaledAgentSpec{OrganizationUrl: organizationUrl, PoolName: poolName}
			id, err := service.GetPoolIdFromName(context.Background(), pat, httpClient, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(expectedPoolId))
		})
	})

	When("Running against the fake platform server API", func() {
		var serverPort int

		BeforeEach(func() {
			var err error
			serverPort, err = GetFreeLocalPort(serverPortRangeStart, serverPortRangeEnd)
			Expect(err).ToNot(HaveOccurred())
			server = fake_platform_server.NewFakeAzurePipelinesPlatformServer()
			err = server.Start(serverPort)
			Expect(err).ToNot(HaveOccurred())

			server.CreatePool(serverPoolId, serverPoolName)
		})

		AfterEach(func() {
			err := server.Stop()
			Expect(err).ToNot(HaveOccurred())
		})

		It("GetPoolIdFromName() should return the id of a prepared pool", func() {
			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
			}
			id, err := service.GetPoolIdFromName(context.Background(), "", httpClient, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(id).To(Equal(int64(serverPoolId)))
		})

		It("CreateOrUpdateDummyAgents() should register the expected number of agents", func() {
			agentCapabilities1 := map[string]string{"foo": "1"}
			agentCapabilities2 := map[string]string{"bar": "1"}
			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
				PodsWithCapabilities: []v1.PodsWithCapabilities{
					{
						Capabilities: agentCapabilities1,
					},
					{
						Capabilities: agentCapabilities2,
					},
				},
			}

			computedAgentName1 := "dummy-agent-dd123f790c2565f2"
			computedAgentName2 := "dummy-agent-9a79b19baa523b11"

			for i := 0; i < 2; i++ {
				// Expect that on the second(!) call, the server will not be called again
				agentDummyNames, err := service.CreateOrUpdateDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, "CR name", &agentSpec)
				Expect(err).ToNot(HaveOccurred())
				Expect(agentDummyNames).To(Equal([]string{computedAgentName1, computedAgentName2}))
				Expect(server.Requests).To(HaveLen(2))
				Expect(server.Requests[0].Type).To(Equal(fake_platform_server.CreateAgent))
				Expect(server.Requests[0].AgentName).To(Equal(computedAgentName1))
				Expect(server.Requests[0].AgentCapabilities).To(Equal(agentCapabilities1))
				Expect(server.Requests[1].Type).To(Equal(fake_platform_server.CreateAgent))
				Expect(server.Requests[1].AgentName).To(Equal(computedAgentName2))
				Expect(server.Requests[1].AgentCapabilities).To(Equal(agentCapabilities2))
			}

			// Now, change the CR and expect that the server will be called again
			agentCapabilities3 := map[string]string{
				"qux": "1",
			}
			agentSpec.PodsWithCapabilities = append(agentSpec.PodsWithCapabilities, v1.PodsWithCapabilities{
				Capabilities: agentCapabilities3,
			})
			agentDummyNamesNew, err := service.CreateOrUpdateDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, "CR name", &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			computedAgentName3 := "dummy-agent-1e27aa07b29fcd08"
			Expect(agentDummyNamesNew).To(Equal([]string{computedAgentName1, computedAgentName2, computedAgentName3}))
			Expect(server.Requests).To(HaveLen(5))
			Expect(server.Requests[2].Type).To(Equal(fake_platform_server.ReplaceAgent))
			Expect(server.Requests[2].AgentName).To(Equal(computedAgentName1))
			Expect(server.Requests[3].Type).To(Equal(fake_platform_server.ReplaceAgent))
			Expect(server.Requests[3].AgentName).To(Equal(computedAgentName2))
			Expect(server.Requests[4].Type).To(Equal(fake_platform_server.CreateAgent))
			Expect(server.Requests[4].AgentName).To(Equal(computedAgentName3))
			Expect(server.Requests[4].AgentCapabilities).To(Equal(agentCapabilities3))
		})

		It("GetPendingJobs() should return an empty list if there are no jobs", func() {
			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
			}
			jobs, err := service.GetPendingJobs(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs.PendingJobs).To(BeEmpty())
		})

		It("GetPendingJobs() should return two jobs that have different demands", func() {
			jobId1 := 1
			jobId2 := 2
			demands1 := map[string]string{
				"foo": "1",
			}
			demands2 := map[string]string{
				"bar": "1",
			}
			duration := int64(30 * time.Second)
			err := server.AddJob(jobId1, serverPoolId, duration, 0, demands1)
			Expect(err).ToNot(HaveOccurred())
			err = server.AddJob(jobId2, serverPoolId, duration, 0, demands2)
			Expect(err).ToNot(HaveOccurred())

			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
			}
			jobs, err := service.GetPendingJobs(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs.PendingJobs).To(HaveLen(2))
			expectedPendingJob1 := service.PendingJob{
				RequestID: jobId1,
				Demands:   demands1,
			}
			Expect(jobs.GetInexactMatch(&demands1)).To(Equal(&map[string][]service.PendingJob{
				"foo=1": {expectedPendingJob1},
			}))
			expectedPendingJob2 := service.PendingJob{
				RequestID: jobId2,
				Demands:   demands2,
			}
			Expect(jobs.GetInexactMatch(&demands2)).To(Equal(&map[string][]service.PendingJob{
				"bar=1": {expectedPendingJob2},
			}))
		})

		It("GetPendingJobs() should return two jobs that have different demands, with a third job that has another demand", func() {
			jobId1 := 1
			jobId2 := 2
			jobId3 := 3
			demands1and2 := map[string]string{
				"foo": "1",
			}
			demands3 := map[string]string{
				"bar": "1",
			}
			duration := int64(30 * time.Second)
			err := server.AddJob(jobId1, serverPoolId, duration, 0, demands1and2)
			Expect(err).ToNot(HaveOccurred())
			err = server.AddJob(jobId2, serverPoolId, duration, 0, demands1and2)
			Expect(err).ToNot(HaveOccurred())
			err = server.AddJob(jobId3, serverPoolId, duration, 0, demands3)
			Expect(err).ToNot(HaveOccurred())

			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
			}
			jobs, err := service.GetPendingJobs(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs.PendingJobs).To(HaveLen(2))
			expectedPendingJob1 := service.PendingJob{
				RequestID: jobId1,
				Demands:   demands1and2,
			}
			expectedPendingJob2 := service.PendingJob{
				RequestID: jobId2,
				Demands:   demands1and2,
			}
			Expect(jobs.GetInexactMatch(&demands1and2)).To(Equal(&map[string][]service.PendingJob{
				"foo=1": {expectedPendingJob1, expectedPendingJob2},
			}))
			expectedPendingJob3 := service.PendingJob{
				RequestID: jobId3,
				Demands:   demands3,
			}
			Expect(jobs.GetInexactMatch(&demands3)).To(Equal(&map[string][]service.PendingJob{
				"bar=1": {expectedPendingJob3},
			}))
		})

		It("DeleteDeadDummyAgents() should delete the expected agents", func() {
			// Create fake agents: first, two "regular" agents (one is online, one is offline)
			crName := "CR"
			offlineAgent1Name := fmt.Sprintf("%s-agent-1", crName)
			server.Agents = append(server.Agents, fake_platform_server.Agent{
				Name:         offlineAgent1Name,
				ID:           1,
				Capabilities: map[string]string{"foo": "1"},
				Status:       "offline",
				CreatedOn:    time.Now(),
			})
			server.Agents = append(server.Agents, fake_platform_server.Agent{
				Name:         fmt.Sprintf("%s-agent-1", crName),
				ID:           2,
				Capabilities: map[string]string{"foo": "1"},
				Status:       "online",
				CreatedOn:    time.Now(),
			})

			// Create offline dummy agents, based on the CR
			agentSpec := v1.AutoScaledAgentSpec{
				OrganizationUrl: fmt.Sprintf("http://localhost:%d", serverPort),
				PoolName:        serverPoolName,
				PodsWithCapabilities: []v1.PodsWithCapabilities{
					{
						Capabilities: map[string]string{"foo": "1"},
					},
					{
						Capabilities: map[string]string{"bar": "1"},
					},
				},
				DummyAgentGarbageCollectionInterval: durationMustParse("0s"), // disable the throttling-protection
				DummyAgentDeletionMinAge:            durationMustParse("1h"),
				NormalOfflineAgentDeletionMinAge:    durationMustParse("1h"),
			}
			agentDummyNames, err := service.CreateOrUpdateDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, crName, &agentSpec)
			Expect(err).ToNot(HaveOccurred())
			computedAgentName1 := "dummy-agent-dd123f790c2565f2"
			computedAgentName2 := "dummy-agent-9a79b19baa523b11"
			Expect(agentDummyNames).To(Equal([]string{computedAgentName1, computedAgentName2}))
			Expect(server.Requests).To(HaveLen(2))
			Expect(server.Requests[0].Type).To(Equal(fake_platform_server.CreateAgent))
			Expect(server.Requests[1].Type).To(Equal(fake_platform_server.CreateAgent))

			// Call DeleteDeadDummyAgents(), because none of the agents are older than DummyAgentDeletionMinAge, nothing should be deleted
			err = service.DeleteDeadDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec, crName, agentDummyNames)
			Expect(err).ToNot(HaveOccurred())
			Expect(server.Requests).To(HaveLen(3))
			Expect(server.Requests[2].Type).To(Equal(fake_platform_server.ListAgent))

			// Pretend that the first agent was created 2 hours ago
			server.Agents[0].CreatedOn = time.Now().Add(-2 * time.Hour)

			// As a consequence, that first agent should be deleted
			err = service.DeleteDeadDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec, crName, agentDummyNames)
			Expect(err).ToNot(HaveOccurred())
			Expect(server.Requests).To(HaveLen(5))
			Expect(server.Requests[3].Type).To(Equal(fake_platform_server.ListAgent))
			Expect(server.Requests[4].Type).To(Equal(fake_platform_server.DeleteAgent))
			Expect(server.Requests[4].AgentName).To(Equal(offlineAgent1Name))
			Expect(server.Agents).To(HaveLen(3))

			// Pretend that the last agent (the 2nd dummy agent) was created 2 hours ago and is no longer required
			dummyAgent2Name := agentDummyNames[1]
			agentDummyNames = agentDummyNames[:len(agentDummyNames)-1]
			server.Agents[2].CreatedOn = time.Now().Add(-2 * time.Hour)

			// As a consequence, that last agent should be deleted
			err = service.DeleteDeadDummyAgents(context.Background(), serverPoolId, fakePat, httpClient, &agentSpec, "CR name", agentDummyNames)
			Expect(err).ToNot(HaveOccurred())
			Expect(server.Requests).To(HaveLen(7))
			Expect(server.Requests[5].Type).To(Equal(fake_platform_server.ListAgent))
			Expect(server.Requests[6].Type).To(Equal(fake_platform_server.DeleteAgent))
			Expect(server.Requests[6].AgentName).To(Equal(dummyAgent2Name))
			Expect(server.Agents).To(HaveLen(2))
		})
	})
})
