package controller_test

import (
	"bytes"
	"fmt"
	azurepipelinesk8sscaleriov1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/third_party/helm"
	"strings"
	"time"
)

/*
Contains end-to-end tests for the controller, inspired by
https://github.com/kubernetes-sigs/kubebuilder/blob/master/docs/book/src/cronjob-tutorial/testdata/project/internal/controller/cronjob_controller_test.go
*/

const controllerNamespace = "azp-operator"
const serverPortRangeStart = 8180
const virtualServerHostname = "fake-platform-server"
const unlimitedInt = 1000000

var controllerChartInstalled = false

type ExpectedRequestType struct {
	Type          fake_platform_server.RequestType
	Min           int
	Max           int
	foundRequests []fake_platform_server.Request
}

func installControllerChart() error {
	manager := helm.New(envCfg.KubeconfigFile())
	c, err := os.Getwd()
	if err != nil {
		return err
	}
	err = manager.RunInstall(
		helm.WithName(kindClusterName),
		helm.WithNamespace(controllerNamespace),
		helm.WithArgs(
			"--set", "image.registry=",
			"--set", "image.repository="+localControllerImage,
			"--set", "image.tag=latest",
			"--set", "image.pullPolicy=IfNotPresent",
			"--set", "useLeaderElection=false", // speeds up the tests
		),
		helm.WithChart(filepath.Join(c, "..", "..", "charts", "azp-k8s-agent-scaler-operator")),
		helm.WithWait(),
		helm.WithTimeout("3m"))

	if err == nil {
		controllerChartInstalled = true
	}

	return err
}

func uninstallControllerChart() error {
	manager := helm.New(envCfg.KubeconfigFile())
	err := manager.RunUninstall(
		helm.WithName(kindClusterName),
		helm.WithNamespace(controllerNamespace),
		helm.WithWait(),
		helm.WithTimeout("3m"))

	return err
}

func durationMustParse(s string) *metav1.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return &metav1.Duration{Duration: d}
}

func getPodLogs(pod corev1.Pod) string {
	podLogOpts := corev1.PodLogOptions{}
	// creates the clientset
	clientset, err := kubernetes.NewForConfig(envCfg.Client().RESTConfig())
	if err != nil {
		return "error getting access to K8S"
	}
	req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return "error in opening stream"
	}
	defer func(podLogs io.ReadCloser) {
		_ = podLogs.Close()
	}(podLogs)

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return "error in copy information from podLogs to buf"
	}
	str := buf.String()

	return str
}

func checkRequests(requests []fake_platform_server.Request, startIndex int, expectedRequestTypes []ExpectedRequestType) error {
	currentIndex := startIndex
	for i, expectedRequestType := range expectedRequestTypes {
		// Verify that the next expectedRequestType.Min up to expectedRequestType.Max requests in "requests" are of the expected type
		for j := 0; j < expectedRequestType.Max; j++ {
			if currentIndex >= len(requests) {
				if i == len(expectedRequestTypes)-1 && j >= expectedRequestType.Min {
					return nil
				}
				return fmt.Errorf("reached the end of requests array, but expected more requests to come (indices: expectedRequestTypes=%d, j=%d)", i, j)
			}
			if requests[currentIndex].Type == expectedRequestType.Type {
				expectedRequestTypes[i].foundRequests = append(expectedRequestTypes[i].foundRequests, requests[currentIndex])
				currentIndex++
			} else {
				// Since expectedRequestType.Max is an upper bound, this is not always an error, because the NEXT
				// ExpectedRequestType type might match!
				// But that is only acceptable if j is past expectedRequestType.Min
				if j >= expectedRequestType.Min {
					if i+1 < len(expectedRequestTypes) {
						if requests[currentIndex].Type == expectedRequestTypes[i+1].Type {
							break
						}
					} else {
						return nil
					}
				}

				return fmt.Errorf("mismatch at indices requests-currentIndex=%d, expectedRequestTypes=%d, j=%d", currentIndex, i, j)
			}
		}
	}

	return nil
}

// checkInitialControllerManagerCallsAgainstFakePlatformServer checks that the
// expected server requests were made by the controller-manager: first,
// GetPoolId, then CreateAgent (for the dummy agent), the ListAgent (the garbage
// collection of dead dummy agents), then ListJob for 1-N times
func checkInitialControllerManagerCallsAgainstFakePlatformServer() (bool, error) {
	if len(server.Requests) >= 4 {
		expectedRequestTypes := []ExpectedRequestType{
			{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
			{Type: fake_platform_server.CreateAgent, Min: 1, Max: unlimitedInt}, // for the dummy agent(s
			{Type: fake_platform_server.ListAgent, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
		}
		err := checkRequests(server.Requests, 0, expectedRequestTypes)
		if err == nil {
			return true, nil
		}
	}
	return false, fmt.Errorf("not enough requests made by the controller-manager yet. Requests made so far:\n%#v", server.Requests)
}

func hasNumberXofPods(number int) (bool, error) {
	podList := &corev1.PodList{}
	err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
	if err != nil {
		return false, err
	}
	return len(podList.Items) == number, nil
}

var hasOnePod = func() (bool, error) { return hasNumberXofPods(1) }
var hasZeroPods = func() (bool, error) { return hasNumberXofPods(0) }

type ContainerState string

const (
	ContainerStateWaiting          ContainerState = "Waiting"
	ContainerStateRunning          ContainerState = "Running"
	ContainerStateWaitingOrRunning ContainerState = "WaitingOrRunning"
	ContainerStateTerminated       ContainerState = "Terminated"
)

var _ = Describe("AutoscaledagentController End-to-end tests", func() {
	var agentContainer corev1.Container
	var autoScaledAgent *azurepipelinesk8sscaleriov1.AutoScaledAgent
	var preStopLifecycleHandler *corev1.Lifecycle
	var checkContainerHasState = func(podIndex, containerIndex int, state ContainerState) (bool, error) {
		podList := &corev1.PodList{}
		err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
		if err != nil {
			return false, err
		}
		if len(podList.Items) <= podIndex {
			return false, fmt.Errorf("podIndex out of range")
		}

		statusIndex, err := service.GetContainerStatusIndex(&podList.Items[podIndex], &autoScaledAgent.Spec, containerIndex)
		if err != nil {
			return false, err
		}
		switch state {
		case ContainerStateWaiting:
			return podList.Items[podIndex].Status.ContainerStatuses[statusIndex].State.Waiting != nil, nil
		case ContainerStateRunning:
			return podList.Items[podIndex].Status.ContainerStatuses[statusIndex].State.Running != nil, nil
		case ContainerStateWaitingOrRunning:
			return podList.Items[podIndex].Status.ContainerStatuses[statusIndex].State.Waiting != nil ||
				podList.Items[podIndex].Status.ContainerStatuses[statusIndex].State.Running != nil, nil
		case ContainerStateTerminated:
			return podList.Items[podIndex].Status.ContainerStatuses[statusIndex].State.Terminated != nil, nil
		default:
			return false, fmt.Errorf("unknown ContainerState")
		}
	}
	var checkContainerHasTerminated = func(podIndex, containerIndex int) (bool, error) {
		return checkContainerHasState(podIndex, containerIndex, ContainerStateTerminated)
	}
	var checkContainerIsWaitingOrRunning = func(podIndex, containerIndex int) (bool, error) {
		return checkContainerHasState(podIndex, containerIndex, ContainerStateWaitingOrRunning)
	}
	var checkAgentContainerHasTerminated = func() (bool, error) { return checkContainerHasTerminated(0, 0) }
	var checkAgentContainerIsWaitingOrRunning = func() (bool, error) { return checkContainerIsWaitingOrRunning(0, 0) }
	var checkFirstSidecarContainerHasTerminated = func() (bool, error) { return checkContainerHasTerminated(0, 1) }

	BeforeEach(func() {
		err := installControllerChart()
		Expect(err).NotTo(HaveOccurred())
		server.Reset()
		server.CreatePool(azpPoolId, azpPoolName)

		// Define an AutoScaledAgent CR as a foundation, to be used (and further customized) by all tests
		serverPort := serverPortRangeStart + GinkgoParallelProcess()
		// Note: we need to use Kubernetes fully-qualified host name, because the ktunnel proxy service runs in a
		// different namespace than the Pods created by the controller-manager
		organizationUrl := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", virtualServerHostname, controllerNamespace, serverPort)
		preStopLifecycleHandler = &corev1.Lifecycle{
			PreStop: &corev1.LifecycleHandler{
				Exec: &corev1.ExecAction{
					Command: []string{
						"/bin/sh",
						"-c",
						"while [ $(pgrep -l Agent.Worker | wc -l) -ne 0 ]; do sleep 1; done",
					},
				},
			},
		}
		agentContainer = corev1.Container{
			Name:            "azure-pipelines-agent",
			Image:           localFakeAgentImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Lifecycle:       preStopLifecycleHandler,
		}
		autoScaledAgent = &azurepipelinesk8sscaleriov1.AutoScaledAgent{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test",
				Namespace: testNamespace,
			},
			Spec: azurepipelinesk8sscaleriov1.AutoScaledAgentSpec{
				PoolName:                            azpPoolName,
				OrganizationUrl:                     organizationUrl,
				PersonalAccessTokenSecretName:       azpSecretName,
				MaxTerminatedPodsToKeep:             &[]int32{0}[0],
				DummyAgentGarbageCollectionInterval: durationMustParse("1h"),
				DummyAgentDeletionMinAge:            durationMustParse("1h"),
				NormalOfflineAgentDeletionMinAge:    durationMustParse("1h"),
				PodsWithCapabilities: []azurepipelinesk8sscaleriov1.PodsWithCapabilities{
					{
						Capabilities: map[string]string{},
						MinCount:     &[]int32{0}[0],
						MaxCount:     &[]int32{2}[0],
						PodTemplateSpec: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								TerminationGracePeriodSeconds: &[]int64{1200}[0],
								ShareProcessNamespace:         &[]bool{true}[0],
								Containers:                    []corev1.Container{agentContainer},
							},
						},
					},
				},
			},
		}
	})

	AfterEach(func() {
		if CurrentSpecReport().Failed() {
			// Get the logs from the controller-manager pod
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(controllerNamespace))
			Expect(err).NotTo(HaveOccurred())
			Expect(len(podList.Items)).To(Equal(2)) // controller-manager and ktunnel pod
			pod := podList.Items[0]
			if !strings.HasPrefix(pod.Name, "controller-manager") {
				pod = podList.Items[1]
			}
			AddReportEntry("controller-manager logs", getPodLogs(pod))

			// Likewise, get the logs of all other pods in the testNamespace
			podList = &corev1.PodList{}
			err = k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
			Expect(err).NotTo(HaveOccurred())
			for _, pod := range podList.Items {
				AddReportEntry(fmt.Sprintf("Pod %s logs", pod.Name), getPodLogs(pod))
			}
		}

		if controllerChartInstalled {
			// Note: uninstalling the chart also deletes the AutoScaledAgent CRD, thus Kubernetes
			// deletes all deployed CRs, and consequently, all agent Pods owned by the CR
			err := uninstallControllerChart()
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("Testing one regular job (without sidecar)", func() {
		// 1. Deploy CR
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise a matching job (10 seconds duration), expect that the pod is created within 6 seconds
		// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
		jobDuration := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), map[string]string{})
		Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

		// Check that the agent container is running for approximately the job's duration
		Consistently(checkAgentContainerIsWaitingOrRunning, jobDuration-2*time.Second, 500*time.Millisecond).Should(BeTrue())

		// 3. Once the Pod is running, the fake agent container should terminate itself shortly after the job duration.
		// The Pod should then disappear shortly after.
		Eventually(checkAgentContainerHasTerminated, jobDuration+5*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Wait for Pod to completely disappear
		Eventually(hasZeroPods, jobDuration+5*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	It("Testing multiple jobs (without sidecar)", func() {
		autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{2}[0]
		// We should NOT mix pods that have no capabilities at all with pods that do have some, because then it could
		// happen that a Pod that has some capabilities fetches a job that does not demand for any, which means that
		// the Pod that has no capabilities at all would be unexpectedly idle
		firstPodTemplateCapabilities := map[string]string{"hey": "ho"}
		autoScaledAgent.Spec.PodsWithCapabilities[0].Capabilities = firstPodTemplateCapabilities

		secondPodTemplateCapabilities := map[string]string{"foo": "bar"}
		autoScaledAgent.Spec.PodsWithCapabilities = append(autoScaledAgent.Spec.PodsWithCapabilities, azurepipelinesk8sscaleriov1.PodsWithCapabilities{
			Capabilities: secondPodTemplateCapabilities,
			MinCount:     &[]int32{0}[0],
			MaxCount:     &[]int32{2}[0],
			PodTemplateSpec: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: &[]int64{1200}[0],
					ShareProcessNamespace:         &[]bool{true}[0],
					Containers:                    []corev1.Container{agentContainer},
				},
			},
		})
		// 1. Deploy CR
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise the jobs (10 seconds duration), spread over the two podsWithCapabilities templates , expect that
		// the pods are created within 6 seconds
		// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
		jobDuration := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), firstPodTemplateCapabilities)
		_ = server.AddJob(2, azpPoolId, int64(jobDuration), firstPodTemplateCapabilities)
		_ = server.AddJob(3, azpPoolId, int64(jobDuration), secondPodTemplateCapabilities)
		hasThreePods := func() (bool, error) { return hasNumberXofPods(3) }
		Eventually(hasThreePods, 6*time.Second, 1*time.Second).Should(BeTrue())

		// Check that the agent containers are running for approximately the job's duration
		checkAgentContainersAreWaitingOrRunning := func() (bool, error) {
			res1, err1 := checkContainerIsWaitingOrRunning(0, 0)
			res2, err2 := checkContainerIsWaitingOrRunning(1, 0)
			res3, err3 := checkContainerIsWaitingOrRunning(2, 0)
			if !res1 || err1 != nil {
				return false, err1
			}
			if !res2 || err2 != nil {
				return false, err2
			}
			return res3, err3
		}
		Consistently(checkAgentContainersAreWaitingOrRunning, jobDuration-2*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Wait for Pod to completely disappear
		Eventually(hasZeroPods, jobDuration+5*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	for maxTerminatedPodsToKeep := 0; maxTerminatedPodsToKeep <= 1; maxTerminatedPodsToKeep++ {
		maxTerminatedPodsToKeep := maxTerminatedPodsToKeep // makes no sense, yep, but see https://onsi.github.io/ginkgo/#dynamically-generating-specs
		It("Testing one regular job (with sidecar)", func() {
			// 1. Deploy CR with one static pod-template (with one side container), don't advertise any jobs for
			// 15 seconds (no pods should be created)
			autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers =
				append(autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers, corev1.Container{
					Name:            "sidecar",
					Image:           "busybox:latest",
					ImagePullPolicy: corev1.PullAlways,
					Lifecycle:       preStopLifecycleHandler,
					Command:         []string{"/bin/sh"},
					Args:            []string{"-c", "trap : TERM INT; sleep 9999999999d & wait"},
				})

			autoScaledAgent.Spec.MaxTerminatedPodsToKeep = &[]int32{int32(maxTerminatedPodsToKeep)}[0]

			Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

			// Check that no new pods are created for 15 seconds
			Consistently(hasZeroPods, 15*time.Second, 1*time.Second).Should(BeTrue())

			// Check that the expected server requests were made by the controller-manager
			_, err := checkInitialControllerManagerCallsAgainstFakePlatformServer()
			Expect(err).NotTo(HaveOccurred())

			// 2. Advertise a matching job (10 seconds duration), expect that the pod is created within 6 seconds
			// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
			jobDuration := 10 * time.Second
			serverRequestIndexPriorToAddJob := len(server.Requests)
			_ = server.AddJob(1, azpPoolId, int64(jobDuration), map[string]string{})
			Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

			// Check that the agent container is running for approximately the job's duration
			Consistently(checkAgentContainerIsWaitingOrRunning, jobDuration-2*time.Second, 500*time.Millisecond).Should(BeTrue())

			// 3. Once the Pod is running, the fake agent container should terminate shortly after the job duration. Then we should observe
			// the termination of the sidecar container
			Eventually(checkAgentContainerHasTerminated, jobDuration+5*time.Second, 500*time.Millisecond).Should(BeTrue())

			Eventually(checkFirstSidecarContainerHasTerminated, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

			// if maxTerminatedPodsToKeep is 0, shortly after the pod should disappear
			if maxTerminatedPodsToKeep == 0 {
				Eventually(hasZeroPods, 5*time.Second, 500*time.Millisecond).Should(BeTrue())
			} else {
				Consistently(hasOnePod, 5*time.Second, 500*time.Millisecond).Should(BeTrue())

				// The Pod's final phase should be Failed
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
				Expect(err).NotTo(HaveOccurred())
				Expect(podList.Items[0].Status.Phase).To(Equal(corev1.PodFailed))
			}

			expectedRequestTypes := []ExpectedRequestType{
				{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
				{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
				{Type: fake_platform_server.CreateAgent, Min: 1, Max: 1},
				{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
				{Type: fake_platform_server.AssignJob, Min: 1, Max: 1},
				{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
				{Type: fake_platform_server.FinishJob, Min: 1, Max: 1},
				{Type: fake_platform_server.DeleteAgent, Min: 1, Max: 1},
				{Type: fake_platform_server.ListJob, Min: 0, Max: unlimitedInt},
			}
			err = checkRequests(server.Requests, serverRequestIndexPriorToAddJob, expectedRequestTypes)
			Expect(err).NotTo(HaveOccurred())

			// Verify that the agent's name (of the CreateAgent request) is used in the AssignJob, FinishJob and DeleteAgent request
			agentName := expectedRequestTypes[2].foundRequests[0].AgentName
			Expect(agentName).NotTo(BeEmpty())
			Expect(expectedRequestTypes[4].foundRequests[0].AgentName).To(Equal(agentName))
			Expect(expectedRequestTypes[6].foundRequests[0].AgentName).To(Equal(agentName))
			Expect(expectedRequestTypes[7].foundRequests[0].AgentName).To(Equal(agentName))
		})
	}
})