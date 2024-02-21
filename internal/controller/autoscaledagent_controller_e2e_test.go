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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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
const untilEnd = 999999

var controllerChartInstalled = false

// ExpectedRequestType defines a sort of "search filter" that looks for <Min> to <Max> consecutive requests of Type.
// For Max, a value of "untilEnd" means "up to the end of the requests array", whereas a value of "unlimitedInt" means
// "as many as possible".
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

// checkRequests verifies that the requests array elements (considering the
// elements from startIndex to the end) contain the expectedRequestTypes in the
// expected order
func checkRequests(requests []fake_platform_server.Request, startIndex int, expectedRequestTypes []ExpectedRequestType) error {
	currentIndex := startIndex
	for i, expectedRequestType := range expectedRequestTypes {
		// Verify that the next expectedRequestType.Min up to expectedRequestType.Max requests in "requests" are of the expected type
		for j := 0; j < expectedRequestType.Max; j++ {
			minRequestCountReached := j >= expectedRequestType.Min
			if currentIndex >= len(requests) {
				hasReachedLastExpectedRequestType := i == len(expectedRequestTypes)-1
				if hasReachedLastExpectedRequestType && minRequestCountReached {
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
				// But that is only acceptable if minRequestCountReached holds
				if minRequestCountReached {
					hasMoreExpectedRequestTypes := i+1 < len(expectedRequestTypes)
					if hasMoreExpectedRequestTypes {
						if requests[currentIndex].Type == expectedRequestTypes[i+1].Type {
							break
						}
					} else {
						if expectedRequestType.Max != untilEnd {
							return nil
						}
					}
				}

				return fmt.Errorf("mismatch at indices requests-currentIndex=%d, expectedRequestTypes=%d, j=%d, startIndex=%d\nrequests:\n%#v", currentIndex, i, j, startIndex, requests)
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

func getPodInTestNamespace(podIndex int) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
	if err != nil {
		return nil, err
	}
	if len(podList.Items) <= podIndex {
		return nil, fmt.Errorf("podIndex out of range")
	}
	return &podList.Items[podIndex], nil
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

func intersection(pS ...[]string) []string {
	hash := make(map[string]*int) // value, counter
	result := make([]string, 0)
	for _, slice := range pS {
		duplicationHash := make(map[string]bool) // duplication checking for individual slice
		for _, value := range slice {
			if _, isDup := duplicationHash[value]; !isDup { // is not duplicated in slice
				if counter := hash[value]; counter != nil { // is found in hash counter map
					if *counter++; *counter >= len(pS) { // is found in every slice
						result = append(result, value)
					}
				} else { // not found in hash counter map
					i := 1
					hash[value] = &i
				}
				duplicationHash[value] = true
			}
		}
	}
	return result
}

var _ = Describe("AutoscaledagentController End-to-end tests", func() {
	var agentMinIdlePeriodDefault = &metav1.Duration{Duration: 1 * time.Second}
	var agentContainer corev1.Container
	var workspaceVolume corev1.Volume
	var autoScaledAgent *azurepipelinesk8sscaleriov1.AutoScaledAgent
	var preStopLifecycleHandler *corev1.Lifecycle
	var checkContainerHasState = func(podIndex, containerIndex int, state ContainerState) (bool, error) {
		pod, err := getPodInTestNamespace(podIndex)
		if err != nil {
			return false, err
		}

		statusIndex, err := service.GetContainerStatusIndex(pod, &autoScaledAgent.Spec, containerIndex)
		if err != nil {
			return false, err
		}
		switch state {
		case ContainerStateWaiting:
			return pod.Status.ContainerStatuses[statusIndex].State.Waiting != nil, nil
		case ContainerStateRunning:
			return pod.Status.ContainerStatuses[statusIndex].State.Running != nil, nil
		case ContainerStateWaitingOrRunning:
			return pod.Status.ContainerStatuses[statusIndex].State.Waiting != nil ||
				pod.Status.ContainerStatuses[statusIndex].State.Running != nil, nil
		case ContainerStateTerminated:
			return pod.Status.ContainerStatuses[statusIndex].State.Terminated != nil, nil
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
		workspaceVolumeName := "workspace"
		agentContainer = corev1.Container{
			Name:            "azure-pipelines-agent",
			Image:           localFakeAgentImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Lifecycle:       preStopLifecycleHandler,
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      workspaceVolumeName,
					MountPath: "/azp/_work",
				},
			},
		}
		workspaceVolume = corev1.Volume{
			Name: workspaceVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
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
				AgentMinIdlePeriod:                  agentMinIdlePeriodDefault,
				DummyAgentGarbageCollectionInterval: &metav1.Duration{Duration: 1 * time.Hour},
				DummyAgentDeletionMinAge:            &metav1.Duration{Duration: 1 * time.Hour},
				NormalOfflineAgentDeletionMinAge:    &metav1.Duration{Duration: 1 * time.Hour},
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
								Volumes:                       []corev1.Volume{workspaceVolume},
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

			// However, the E2E test suite sometimes has weird issues where unexpected calls to the fake platform server
			// are made, so I suspect that the clean up takes some time, so let's sleep for a bit to give the K8s
			// control plane time to clean up
			time.Sleep(10 * time.Second)
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
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
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
					Volumes:                       []corev1.Volume{workspaceVolume},
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
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, firstPodTemplateCapabilities)
		_ = server.AddJob(2, azpPoolId, int64(jobDuration), 0, 0, firstPodTemplateCapabilities)
		_ = server.AddJob(3, azpPoolId, int64(jobDuration), 0, 0, secondPodTemplateCapabilities)
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
		It(fmt.Sprintf("Testing one regular job (with sidecar, maxTerminatedPodsToKeep=%d))", maxTerminatedPodsToKeep), func() {
			// 1. Deploy CR with one static pod-template (with one side container), don't advertise any jobs for
			// 15 seconds (no pods should be created)
			autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers =
				append(autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers, corev1.Container{
					Name:            "a-sidecar", // use a name that comes before "azure-pipelines-agent" in the alphabet, to test GetContainerStatusIndex()
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
			_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
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

	It("Job cancellation (before it starts)", func() {
		// 1. Deploy CR
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise a job that has a high start-up delay (30 seconds), expect that the pod is created within 6 seconds
		// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
		serverRequestIndexPriorToAddJob := len(server.Requests)
		jobDuration := 10 * time.Second
		preStartDelay := 30 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), int64(preStartDelay), 0, map[string]string{})
		Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

		// Continuously for 10 seconds, verify that the fake platform server does not have any AssignJob requests yet
		expectedRequestTypes := []ExpectedRequestType{
			{Type: fake_platform_server.ListJob, Min: 0, Max: unlimitedInt},
			{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
			{Type: fake_platform_server.CreateAgent, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: untilEnd},
		}
		Consistently(func() error {
			return checkRequests(server.Requests, serverRequestIndexPriorToAddJob, expectedRequestTypes)
		}, 10*time.Second, 500*time.Millisecond).ShouldNot(HaveOccurred())

		// 3. Cancel the job, the pod should disappear shortly after
		err := server.CancelJob(1)
		Expect(err).NotTo(HaveOccurred())

		Eventually(hasZeroPods, jobDuration+15*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	It("Job cancellation (while running)", func() {
		// 1. Deploy CR
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise a job (duration 30 seconds), expect that the pod is created within 6 seconds
		// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
		serverRequestIndexPriorToAddJob := len(server.Requests)
		jobDuration := 30 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

		// The pod should continue running for 10 seconds
		Consistently(checkAgentContainerIsWaitingOrRunning, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Verify the requests made by the agent container
		expectedRequestTypes := []ExpectedRequestType{
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
			{Type: fake_platform_server.CreateAgent, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.AssignJob, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: untilEnd},
		}
		err := checkRequests(server.Requests, serverRequestIndexPriorToAddJob, expectedRequestTypes)
		Expect(err).NotTo(HaveOccurred())

		// 3. Cancel the job, the pod should disappear shortly after
		serverRequestIndexPriorToCancelJob := len(server.Requests)
		err = server.CancelJob(1)
		Expect(err).NotTo(HaveOccurred())

		Eventually(hasZeroPods, 6*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Verify the requests made by the agent container
		expectedRequestTypes = []ExpectedRequestType{
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.DeleteAgent, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 0, Max: untilEnd},
		}
		err = checkRequests(server.Requests, serverRequestIndexPriorToCancelJob, expectedRequestTypes)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Job cancellation (unschedulable)", func() {
		// 1. Deploy CR, using an unschedulable node taint
		autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.NodeSelector = map[string]string{"some-label": "that-does-not-exist"}
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise a job (duration 30 seconds), expect that the pod is created within 6 seconds
		// (6 seconds because the controller manager queries the API every 5 seconds, +1 second to reduce flakiness)
		serverRequestIndexPriorToAddJob := len(server.Requests)
		jobDuration := 30 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

		// The pod should be unscheduled for 10 seconds
		Consistently(func() (bool, error) {
			pod, err := getPodInTestNamespace(0)
			if err != nil {
				return false, err
			}
			return pod.Status.Phase == corev1.PodPending, nil
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Verify the requests made only by the controller
		expectedRequestTypes := []ExpectedRequestType{
			{Type: fake_platform_server.ListJob, Min: 1, Max: untilEnd},
		}
		err := checkRequests(server.Requests, serverRequestIndexPriorToAddJob, expectedRequestTypes)
		Expect(err).NotTo(HaveOccurred())

		// 3. Cancel the job, the pod should disappear shortly after
		err = server.CancelJob(1)
		Expect(err).NotTo(HaveOccurred())

		Eventually(hasZeroPods, 6*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	It("Exceed maxCount", func() {
		// 1. Deploy CR
		maxCount := 2
		autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{int32(maxCount)}[0]
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise three jobs (each with a duration of 10 seconds), expect that two Pods are created, whose
		// name we remember
		jobDuration := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		_ = server.AddJob(2, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		_ = server.AddJob(3, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		hasTwoPods := func() (bool, error) { return hasNumberXofPods(2) }
		Eventually(hasTwoPods, 6*time.Second, 1*time.Second).Should(BeTrue())
		Consistently(hasTwoPods, jobDuration-2*time.Second, 1*time.Second).Should(BeTrue())
		pod1, err := getPodInTestNamespace(0)
		Expect(err).NotTo(HaveOccurred())
		pod2, err := getPodInTestNamespace(1)
		Expect(err).NotTo(HaveOccurred())
		pod1Name := pod1.Name
		pod2Name := pod2.Name

		// 3. Wait for the first two Pods to disappear and a new one to appear
		Eventually(func() (bool, error) {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
			if err != nil {
				return false, err
			}
			if len(podList.Items) != 1 {
				return false, nil
			}
			return podList.Items[0].Name != pod1Name && podList.Items[0].Name != pod2Name, nil
		}, 12*time.Second, 500*time.Millisecond).Should(BeTrue())

		Eventually(hasZeroPods, jobDuration+6*time.Second, 500*time.Millisecond).Should(BeTrue())

		// 4. Ensure that no new pods come up
		Consistently(hasZeroPods, 10*time.Second, 1*time.Second).Should(BeTrue())
	})

	It("minCount with one job", func() {
		// 1. Deploy CR
		minCount := 3
		autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{int32(minCount)}[0]
		autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{999}[0]
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// 2. Wait for the 3 minCount pods to appear. Retrieve and save their names
		hasThreePods := func() (bool, error) { return hasNumberXofPods(3) }
		Eventually(hasThreePods, 6*time.Second, 1*time.Second).Should(BeTrue())
		pod1, err := getPodInTestNamespace(0)
		Expect(err).NotTo(HaveOccurred())
		pod2, err := getPodInTestNamespace(1)
		Expect(err).NotTo(HaveOccurred())
		pod3, err := getPodInTestNamespace(2)
		Expect(err).NotTo(HaveOccurred())

		// 3. Verify that they keep running for 10 seconds
		Consistently(hasThreePods, 10*time.Second, 1*time.Second).Should(BeTrue())

		// 4. Verify that there are no AssignJob requests
		for _, req := range server.Requests {
			Expect(req.Type).NotTo(Equal(fake_platform_server.AssignJob))
		}

		// 5. Schedule 1 job, 10 sec duration
		jobDuration := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})

		// 6. Verify that the pods remain stable during the job duration
		Consistently(func() (bool, error) {
			p1, err := getPodInTestNamespace(0)
			if err != nil {
				return false, err
			}
			p2, err := getPodInTestNamespace(1)
			if err != nil {
				return false, err
			}
			p3, err := getPodInTestNamespace(2)
			if err != nil {
				return false, err
			}
			_, err = getPodInTestNamespace(3)
			if err == nil {
				return false, err // a fourth Pod may not exist
			}
			return p1.Name == pod1.Name && p2.Name == pod2.Name && p3.Name == pod3.Name, nil
		}, jobDuration, 1*time.Second).Should(BeTrue())

		// 7. Verify that there are 3 pods, but one of them has a different name
		originalPodNames := []string{pod1.Name, pod2.Name, pod3.Name}
		Eventually(func() (bool, error) {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
			if err != nil {
				return false, err
			}
			if len(podList.Items) != 3 {
				return false, nil
			}
			currentPodNames := []string{podList.Items[0].Name, podList.Items[1].Name, podList.Items[2].Name}
			return len(intersection(originalPodNames, currentPodNames)) == 2, nil
		}, 6*time.Second, 1*time.Second).Should(BeTrue())
	})

	for _, agentMinIdlePeriod := range []metav1.Duration{*agentMinIdlePeriodDefault, {Duration: 10 * time.Second}} {
		agentMinIdlePeriod := agentMinIdlePeriod // see https://onsi.github.io/ginkgo/#dynamically-generating-specs
		It(fmt.Sprintf("minCount reduction (%v)", agentMinIdlePeriod), func() {
			// 1. Deploy CR
			minCount := 3
			autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{int32(minCount)}[0]
			autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{999}[0]
			autoScaledAgent.Spec.AgentMinIdlePeriod = &agentMinIdlePeriod
			Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

			// 2. Wait for the 3 minCount pods to appear. Retrieve and save their names
			hasThreePods := func() (bool, error) { return hasNumberXofPods(3) }
			Eventually(hasThreePods, 6*time.Second, 1*time.Second).Should(BeTrue())
			pod1, err := getPodInTestNamespace(0)
			Expect(err).NotTo(HaveOccurred())
			pod2, err := getPodInTestNamespace(1)
			Expect(err).NotTo(HaveOccurred())
			pod3, err := getPodInTestNamespace(2)
			Expect(err).NotTo(HaveOccurred())

			// 3. Verify that they keep running for 10 seconds
			Consistently(hasThreePods, 10*time.Second, 1*time.Second).Should(BeTrue())

			// 4. Verify that there are no AssignJob requests
			for _, req := range server.Requests {
				Expect(req.Type).NotTo(Equal(fake_platform_server.AssignJob))
			}

			// 5. Reduce the minCount
			minCount = 1
			autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{int32(minCount)}[0]
			Expect(k8sClient.Update(ctx, autoScaledAgent)).Should(Succeed())

			// 5. Wait for the pod count to have changed to 1, verify that the remaining Pod's name is one
			// of the 3 original pod names. However, during agentMinIdlePeriod, the pod count should remain 3
			Consistently(hasThreePods, agentMinIdlePeriod.Duration, 1*time.Second).Should(BeTrue())
			Eventually(hasOnePod, 15*time.Second, 1*time.Second).Should(BeTrue())
			p1New, err := getPodInTestNamespace(0)
			Expect(err).NotTo(HaveOccurred())
			// TODO remove once we understand why Expect() below sometimes failed (for no apparent reason)
			logf.Log.Info("p1New.Name: " + p1New.Name)
			logf.Log.Info(fmt.Sprintf("intersection: %#v", intersection([]string{p1New.Name}, []string{pod1.Name, pod2.Name, pod3.Name})))
			Expect(intersection([]string{p1New.Name}, []string{pod1.Name, pod2.Name, pod3.Name})).To(HaveLen(1))
			// Note: the down-scaling is somewhat slow. First, the controller-manager needs to assign the
			// IdleAgentPodFirstDetectionTimestampAnnotationKey annotation to the Pod, and only in the next loop the
			// controller-manager deletes a Pod, and it only deletes one Pod per reconciliation loop.
		})
	}

	It("FinishDelay check", func() {
		// 1. Deploy CR
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Advertise a job (duration 10 seconds, FinishDelay 10 seconds), expect that the pod is created within 6 seconds
		serverRequestIndexPriorToAddJob := len(server.Requests)
		jobDuration := 10 * time.Second
		finishDelay := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, int64(finishDelay), map[string]string{})
		Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())

		// The pod should continue running for the job's duration + some extra time (needed for tear-down)
		Consistently(checkAgentContainerIsWaitingOrRunning, jobDuration+1*time.Second, 500*time.Millisecond).Should(BeTrue())

		// Verify the requests made by the agent container
		expectedRequestTypes := []ExpectedRequestType{
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
			{Type: fake_platform_server.CreateAgent, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.AssignJob, Min: 1, Max: 1},
			{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
			{Type: fake_platform_server.FinishJob, Min: 1, Max: untilEnd},
		}
		err := checkRequests(server.Requests, serverRequestIndexPriorToAddJob, expectedRequestTypes)
		Expect(err).NotTo(HaveOccurred())

		// 3. Verify that agent container still lives for the FinishDelay, before the pod disappears
		Consistently(checkAgentContainerIsWaitingOrRunning, finishDelay-1*time.Second, 500*time.Millisecond).Should(BeTrue())
		Eventually(hasZeroPods, 6*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	It("PVC creation and reuse", func() {
		// 1. Deploy CR with a reusable cache directory, and min/maxCount = 2
		cacheVolumeName := "cache-volume"
		autoScaledAgent.Spec.ReusableCacheVolumes = []azurepipelinesk8sscaleriov1.ReusableCacheVolume{
			{
				Name:             cacheVolumeName,
				StorageClassName: "standard",
				RequestedStorage: "1Gi",
			},
		}
		autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers =
			append(autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers, corev1.Container{
				Name:            "sidecar",
				Image:           "busybox:latest",
				ImagePullPolicy: corev1.PullAlways,
				Lifecycle:       preStopLifecycleHandler,
				Command:         []string{"/bin/sh"},
				Args:            []string{"-c", "trap : TERM INT; sleep 9999999999d & wait"},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      cacheVolumeName,
						MountPath: "/cache",
					},
				},
			})
		autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{2}[0]
		autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{999}[0]
		Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

		// Wait until the controller-manager is ready, having made the required calls to the fake platform server
		Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

		// 2. Wait for the 2 minCount pods to appear and that there are two PVCs that have the promised annotation set
		// to the pod names
		hasTwoPods := func() (bool, error) { return hasNumberXofPods(2) }
		Eventually(hasTwoPods, 6*time.Second, 1*time.Second).Should(BeTrue())
		pod1, err := getPodInTestNamespace(0)
		Expect(err).NotTo(HaveOccurred())
		pod2, err := getPodInTestNamespace(1)
		Expect(err).NotTo(HaveOccurred())
		podNames := []string{pod1.Name, pod2.Name}
		pvcList := &corev1.PersistentVolumeClaimList{}
		err = k8sClient.List(ctx, pvcList, client.InNamespace(testNamespace))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(pvcList.Items)).To(Equal(2))
		pvcNames := []string{pvcList.Items[0].Name, pvcList.Items[1].Name}
		promisedPodNames := []string{}
		for _, pvc := range pvcList.Items {
			promisedPodName, exists := pvc.Annotations[service.ReusableCacheVolumePromisedAnnotationKey]
			promisedPodNames = append(promisedPodNames, promisedPodName)
			Expect(exists).To(BeTrue())
			cacheVolumeNameFromAnnotation, exists := pvc.Annotations[service.ReusableCacheVolumeNameAnnotationKey]
			Expect(cacheVolumeNameFromAnnotation).To(Equal(cacheVolumeName))
			Expect(exists).To(BeTrue())
		}
		Expect(intersection(podNames, promisedPodNames)).To(HaveLen(2))

		// Verify that the pods are running
		Consistently(hasTwoPods, 5*time.Second, 500*time.Millisecond).Should(BeTrue())

		// 3. Set minCount to 0, wait until Pods have been deleted
		autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{0}[0]
		Expect(k8sClient.Update(ctx, autoScaledAgent)).Should(Succeed())
		Eventually(hasZeroPods, 16*time.Second, 1*time.Second).Should(BeTrue())

		// 4. Check that the 2 PVCs still exist, but are no longer promised (which takes a bit of time)
		checkTwoUnpromisedPvs := func() (bool, error) {
			pvcList = &corev1.PersistentVolumeClaimList{}
			err = k8sClient.List(ctx, pvcList, client.InNamespace(testNamespace))
			if err != nil {
				return false, err
			}
			if len(pvcList.Items) != 2 {
				return false, nil
			}
			for _, pvc := range pvcList.Items {
				_, exists := pvc.Annotations[service.ReusableCacheVolumePromisedAnnotationKey]
				if exists {
					return false, nil
				}
			}
			return true, nil
		}
		Eventually(checkTwoUnpromisedPvs, 11*time.Second, 500*time.Millisecond).Should(BeTrue())

		// 5. Update maxCount to 2, then schedule 3 jobs, then wait until all pods are
		// gone - at any time, we may never see more than 2 pods, and also no more than 2
		// PVCs
		autoScaledAgent.Spec.PodsWithCapabilities[0].MaxCount = &[]int32{2}[0]
		Expect(k8sClient.Update(ctx, autoScaledAgent)).Should(Succeed())
		jobDuration := 10 * time.Second
		_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		_ = server.AddJob(2, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		_ = server.AddJob(3, azpPoolId, int64(jobDuration), 0, 0, map[string]string{})
		Consistently(func() (bool, error) {
			podList := &corev1.PodList{}
			err := k8sClient.List(ctx, podList, client.InNamespace(testNamespace))
			if err != nil {
				return false, err
			}
			if len(podList.Items) > 2 {
				return false, nil
			}
			pvcList := &corev1.PersistentVolumeClaimList{}
			err = k8sClient.List(ctx, pvcList, client.InNamespace(testNamespace))
			if err != nil {
				return false, err
			}
			if len(pvcList.Items) > 2 {
				return false, nil
			}
			return true, nil
		}, 2*jobDuration+15*time.Second, 1*time.Second).Should(BeTrue())

		Eventually(hasZeroPods, 21*time.Second, 500*time.Millisecond).Should(BeTrue())

		// 6. Verify that the two PVCs are the same ones as before
		pvcList = &corev1.PersistentVolumeClaimList{}
		err = k8sClient.List(ctx, pvcList, client.InNamespace(testNamespace))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(pvcList.Items)).To(Equal(2))
		finalPvcNames := []string{pvcList.Items[0].Name, pvcList.Items[1].Name}
		Expect(intersection(finalPvcNames, pvcNames)).To(HaveLen(2))

		Eventually(checkTwoUnpromisedPvs, 11*time.Second, 500*time.Millisecond).Should(BeTrue())
	})

	for _, hasStaticSidecar := range []bool{false, true} {
		hasStaticSidecar := hasStaticSidecar // see https://onsi.github.io/ginkgo/#dynamically-generating-specs
		It(fmt.Sprintf("ExtraAgentContainers feature (static sidecar: %t)", hasStaticSidecar), func() {
			// 1. Deploy CR with a minCount of 1
			autoScaledAgent.Spec.PodsWithCapabilities[0].MinCount = &[]int32{1}[0]
			if hasStaticSidecar {
				autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers =
					append(autoScaledAgent.Spec.PodsWithCapabilities[0].PodTemplateSpec.Spec.Containers, corev1.Container{
						Name:            "sidecar",
						Image:           "ubuntu:latest",
						ImagePullPolicy: corev1.PullAlways,
						Lifecycle:       preStopLifecycleHandler,
						Command:         []string{"/bin/sh"},
						Args:            []string{"-c", "trap : TERM INT; sleep 9999999999d & wait"},
					})
			}
			Expect(k8sClient.Create(ctx, autoScaledAgent)).Should(Succeed())

			// Wait until the controller-manager is ready, having made the required calls to the fake platform server
			Eventually(checkInitialControllerManagerCallsAgainstFakePlatformServer, 20*time.Second, 1*time.Second).Should(BeTrue())

			// 2. Wait for the pod to be up, note its name
			Eventually(hasOnePod, 6*time.Second, 1*time.Second).Should(BeTrue())
			minCountPod1, err := getPodInTestNamespace(0)
			Expect(err).NotTo(HaveOccurred())

			// Give the agent some time to assign itself to the job (avoiding that serverRequestIndexPriorToAgentStart below
			// starts too early)
			time.Sleep(5 * time.Second)

			// 3. Schedule a job with two sidecar containers, one sets cpu+ram, one does not
			serverRequestIndexPriorToAgentStart := len(server.Requests)
			jobDuration := 15 * time.Second
			eacValue := "name=c1,image=alpine:latest,cpu=250m,memory=64Mi||name=c2,image=busybox:latest"
			_ = server.AddJob(1, azpPoolId, int64(jobDuration), 0, 0,
				map[string]string{service.ExtraAgentContainersAnnotationKey: eacValue})

			// 4. Verify that a second Pod was started, that the sidecar containers are running, and that
			// the ExtraAgentContainers env var is present
			hasTwoPods := func() (bool, error) { return hasNumberXofPods(2) }
			Eventually(hasTwoPods, 6*time.Second, 1*time.Second).Should(BeTrue())
			pod1, err := getPodInTestNamespace(0)
			Expect(err).NotTo(HaveOccurred())
			pod2, err := getPodInTestNamespace(1)
			Expect(err).NotTo(HaveOccurred())
			correctPod := pod1
			if pod1.Name == minCountPod1.Name {
				correctPod = pod2
			}
			expectedLength := 3
			firstEacContainerIndex := 1
			if hasStaticSidecar {
				expectedLength = 4
				firstEacContainerIndex = 2
			}
			Expect(correctPod.Spec.Containers).To(HaveLen(expectedLength))
			Expect(correctPod.Spec.Containers[firstEacContainerIndex].Name).To(Equal("c1"))
			Expect(correctPod.Spec.Containers[firstEacContainerIndex].Image).To(Equal("alpine:latest"))
			Expect(correctPod.Spec.Containers[firstEacContainerIndex].Resources.Requests).To(Equal(corev1.ResourceList{"cpu": resource.MustParse("250m"), "memory": resource.MustParse("64Mi")}))
			Expect(correctPod.Spec.Containers[firstEacContainerIndex+1].Name).To(Equal("c2"))
			Expect(correctPod.Spec.Containers[firstEacContainerIndex+1].Image).To(Equal("busybox:latest"))
			Expect(correctPod.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: service.ExtraAgentContainersAnnotationKey, Value: eacValue}))

			// 5. Wait until the EAC-based agent has assigned itself to the job
			expectedRequestTypes := []ExpectedRequestType{
				{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
				{Type: fake_platform_server.GetPoolId, Min: 1, Max: 1},
				{Type: fake_platform_server.CreateAgent, Min: 1, Max: 1},
				{Type: fake_platform_server.ListJob, Min: 1, Max: unlimitedInt},
				{Type: fake_platform_server.AssignJob, Min: 1, Max: 1},
				{Type: fake_platform_server.ListJob, Min: 0, Max: unlimitedInt},
			}
			Eventually(func() (bool, error) {
				err := checkRequests(server.Requests, serverRequestIndexPriorToAgentStart, expectedRequestTypes)
				if err != nil {
					return false, err
				}
				return correctPod.Name == expectedRequestTypes[4].foundRequests[0].AgentName, nil
			}, 15*time.Second, 1*time.Second).Should(BeTrue())

			// 6. Eventually, that Pod with the EAC container should disappear and the controller should recreate a new Pod
			time.Sleep(2 * jobDuration)
			check, err := hasOnePod()
			Expect(err).NotTo(HaveOccurred())
			Expect(check).To(BeTrue())
			pod1New, err := getPodInTestNamespace(0)
			Expect(err).NotTo(HaveOccurred())
			Expect(pod1New.Name).To(Not(Equal(minCountPod1.Name)))
			Expect(pod1New.Name).To(Not(Equal(correctPod.Name)))
		})
	}
})
