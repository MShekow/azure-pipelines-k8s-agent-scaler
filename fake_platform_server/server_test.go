package fake_platform_server_test

import (
	"fmt"
	utils "github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_agent/utils"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"net/http"
	"time"
)

/*
This is a simple test suite for the fake platform server and parts of the corresponding client code.
*/

const serverPort = 8181

var fakeOrganizationUrl = fmt.Sprintf("http://localhost:%d", serverPort)

var _ = Describe("Empty server", func() {

	var server *fake_platform_server.FakeAzurePipelinesPlatformServer
	var httpClient *http.Client

	BeforeEach(func() {
		server = fake_platform_server.NewFakeAzurePipelinesPlatformServer()
		err := server.Start(serverPort)
		Expect(err).ToNot(HaveOccurred())
		httpClient = &http.Client{
			Timeout: 10 * time.Hour,
		}
	})

	AfterEach(func() {
		err := server.Stop()
		Expect(err).ToNot(HaveOccurred())
	})

	It("initially server returns no pools, after calling CreatePool, server returns the expected pool", func() {
		poolName := "test"
		pools, err := ListPools(httpClient, serverPort, poolName)
		Expect(err).ToNot(HaveOccurred())
		Expect(pools.Value).To(BeEmpty())

		poolId := 5
		server.CreatePool(poolId, poolName)
		pools, err = ListPools(httpClient, serverPort, poolName)
		Expect(err).ToNot(HaveOccurred())
		Expect(pools.Value).To(HaveLen(1))
		Expect(pools.Value[0].ID).To(Equal(poolId))
	})

	It("initially server returns no agents, after calling AddAgent, server returns the expected agent", func() {
		poolName := "test"
		poolId := 5
		server.CreatePool(poolId, poolName)

		agents, err := ListAgents(httpClient, serverPort, poolId)
		Expect(err).ToNot(HaveOccurred())
		Expect(agents.Value).To(BeEmpty())

		agentName := "test-agent"
		agentId, err := utils.RegisterAsAgent(fakeOrganizationUrl, int64(poolId), agentName, httpClient)
		Expect(err).ToNot(HaveOccurred())

		agents, err = ListAgents(httpClient, serverPort, poolId)
		Expect(err).ToNot(HaveOccurred())
		Expect(agents.Value).To(HaveLen(1))
		Expect(agents.Value[0].Id).To(Equal(agentId))
		Expect(agents.Value[0].Name).To(Equal(agentName))
	})

	When("server has one pool and agent", func() {

		var poolName = "test"
		var poolId = 5

		BeforeEach(func() {
			server.CreatePool(poolId, poolName)

			agentName := "test-agent"
			_, err := utils.RegisterAsAgent(fakeOrganizationUrl, int64(poolId), agentName, httpClient)
			Expect(err).ToNot(HaveOccurred())
		})

		It("initially server returns no jobs, after calling AddJob, server returns the expected job", func() {
			jobs, err := utils.GetPendingJobs(fakeOrganizationUrl, int64(poolId), httpClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(BeEmpty())

			jobId := 2
			demands := map[string]string{
				"foo": "bar",
				"qux": "zzz",
			}
			duration := int64(30 * time.Second)
			err = server.AddJob(jobId, poolId, duration, 0, 0, demands)
			Expect(err).ToNot(HaveOccurred())

			jobs, err = utils.GetPendingJobs(fakeOrganizationUrl, int64(poolId), httpClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].ID).To(Equal(jobId))
			Expect(jobs[0].PoolID).To(Equal(poolId))
			Expect(jobs[0].State).To(Equal(fake_platform_server.Pending))
			Expect(jobs[0].Duration).To(Equal(duration))
			Expect(jobs[0].Demands).To(HaveLen(2))
			Expect(jobs[0].Demands[0]).To(Equal("foo -equals bar"))
			Expect(jobs[0].Demands[1]).To(Equal("qux -equals zzz"))

			// Cancel the job, server should no longer return it
			err = server.CancelJob(jobId)
			Expect(err).ToNot(HaveOccurred())
			jobs, err = utils.GetPendingJobs(fakeOrganizationUrl, int64(poolId), httpClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(BeEmpty())
		})

		It("Assigning and finishing a job clears it from the queue", func() {
			jobId := 2
			demands := map[string]string{
				"foo": "bar",
				"qux": "zzz",
			}
			duration := int64(30 * time.Second)
			err := server.AddJob(jobId, poolId, duration, 0, 0, demands)
			Expect(err).ToNot(HaveOccurred())

			agentName := "test-agent"
			err = utils.AssignJob(fakeOrganizationUrl, agentName, jobId, httpClient)
			Expect(err).ToNot(HaveOccurred())

			Expect(server.Jobs).To(HaveLen(1))
			Expect(server.Jobs[0].State).To(Equal(fake_platform_server.InProgress))

			err = utils.FinishJob(fakeOrganizationUrl, agentName, jobId, httpClient)
			Expect(err).ToNot(HaveOccurred())

			Expect(server.Jobs).To(HaveLen(1))
			Expect(server.Jobs[0].State).To(Equal(fake_platform_server.Finished))

			jobs, err := utils.GetPendingJobs(fakeOrganizationUrl, int64(poolId), httpClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(jobs).To(BeEmpty())

			// Verify the requests
			Expect(server.Requests).To(HaveLen(4))
			Expect(server.Requests[0].Type).To(Equal(fake_platform_server.CreateAgent))
			Expect(server.Requests[1].Type).To(Equal(fake_platform_server.AssignJob))
			Expect(server.Requests[2].Type).To(Equal(fake_platform_server.FinishJob))
			Expect(server.Requests[3].Type).To(Equal(fake_platform_server.ListJob))
		})

		It("Assigning a job twice returns an error, and Reset() cleans all data", func() {
			jobId := 2
			demands := map[string]string{
				"foo": "bar",
				"qux": "zzz",
			}
			duration := int64(30 * time.Second)
			err := server.AddJob(jobId, poolId, duration, 0, 0, demands)
			Expect(err).ToNot(HaveOccurred())

			err = utils.AssignJob(fakeOrganizationUrl, "test-agent", jobId, httpClient)
			Expect(err).ToNot(HaveOccurred())

			err = utils.AssignJob(fakeOrganizationUrl, "test-agent", jobId, httpClient)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(&utils.ConflictError{}))

			Expect(server.Requests).To(HaveLen(2))
			Expect(server.Requests[0].Type).To(Equal(fake_platform_server.CreateAgent))
			Expect(server.Requests[1].Type).To(Equal(fake_platform_server.AssignJob))

			server.Reset()

			Expect(server.Requests).To(BeEmpty())
			Expect(server.Jobs).To(BeEmpty())
			Expect(server.Agents).To(BeEmpty())
		})
	})

})
