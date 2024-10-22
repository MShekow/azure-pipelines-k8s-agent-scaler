// Contains the end-to-end tests for the controller.

package controller_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	azurepipelinesk8sscaleriov1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/fake_platform_server"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"os"
	"os/exec"
	"path/filepath"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/support/kind"
	"sigs.k8s.io/e2e-framework/support/utils"
	"strings"
	"syscall"
	"testing"
	"time"

	gomega_format "github.com/onsi/gomega/format"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var kindClusterName string
var envCfg *envconf.Config
var ctx context.Context
var server *fake_platform_server.FakeAzurePipelinesPlatformServer
var tunnel *exec.Cmd

const localControllerImage = "controller"
const localControllerImageTag = "latest"
const localFakeAgentImage = "fake-agent:local"
const azpPoolId = 5
const azpPoolName = "Default"
const azpSecretName = "azp-secret"
const testNamespace = "default"

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

func startK8sTunnel(serverPort int) (*exec.Cmd, error) {
	cmd := exec.CommandContext(
		ctx,
		"ktunnel",
		"expose", virtualServerHostname, fmt.Sprintf("%d:%d", serverPort, serverPort),
		"-n", controllerNamespace,
	)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+envCfg.KubeconfigFile())

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() { // To be able to diagnose errors, print stderr messages continuously
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			GinkgoWriter.Println("ktunnel: " + scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			GinkgoWriter.Println("ktunnel: error reading from stderr:", err)
		}
	}()

	scanner := bufio.NewScanner(stdout)
	found := make(chan bool)

	scannerCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Wait for at most 20 seconds for the tunnel to start
	go func() {
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), fmt.Sprintf("starting tcp tunnel from source %d to target localhost:%d", serverPort, serverPort)) {
				found <- true
				return
			} else {
				GinkgoWriter.Println(scanner.Text())
			}
		}
		if err := scanner.Err(); err != nil {
			found <- false
		}
	}()

	// Wait for either the scanner to find the line or the timeout to occur
	select {
	case <-found:
		// Keep reading from stdout for debugging purposes
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				GinkgoWriter.Println(scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				GinkgoWriter.Println("Error reading from stdout:", err)
			}
		}()
		return cmd, nil
	case <-scannerCtx.Done():
		return nil, errors.New("ktunnel did not start successfully")
	}
}

func stopTunnel(cmd *exec.Cmd) error {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	err := cmd.Wait()
	if err != nil {
		exitError, ok := err.(*exec.ExitError)
		if ok {
			// The command exited with a non-zero status.
			GinkgoWriter.Printf("ktunnel exited with non-zero status: %v\n", exitError.ExitCode())
			GinkgoWriter.Printf("Stderr: %s\n", exitError.Stderr)
		} else {
			// The command failed to run or didn't complete successfully.
			GinkgoWriter.Printf("Command failed: %v\n", err)
		}
	}

	return err
}

func buildLocalImages() error {
	workDir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	buildScriptPath := filepath.Join(workDir, "..", "..", "build-docker-images.sh")
	buildProcess := utils.RunCommand(fmt.Sprintf("%s %s %s",
		buildScriptPath,
		fmt.Sprintf("%s:%s", localControllerImage, localControllerImageTag),
		localFakeAgentImage),
	)
	return buildProcess.Err()
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	gomega_format.MaxLength = 0 // Ensure that debug output is not truncated

	var err error

	err = buildLocalImages()
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")

	envCfg = envconf.New()
	ctx = context.Background()
	kindClusterName = envconf.RandomName("kind", 12)

	kindNodeImage := os.Getenv("KIND_NODE_IMAGE")
	if kindNodeImage == "" {
		kindNodeImage = "kindest/node:v1.28.13"
		logf.Log.Info("Using default KIND node image", "image", kindNodeImage)
	}
	createFunc := envfuncs.CreateClusterWithConfig(kind.NewProvider(), kindClusterName, "testdata/kind-config.yaml", kind.WithImage(kindNodeImage))

	ctx, err = createFunc(ctx, envCfg)
	Expect(err).NotTo(HaveOccurred())

	cfg = envCfg.Client().RESTConfig()

	err = azurepipelinesk8sscaleriov1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// load the locally-built images into the kind cluster
	loadImgFunc := envfuncs.LoadImageToCluster(
		kindClusterName,
		fmt.Sprintf("%s:%s", localControllerImage, localControllerImageTag),
	)
	ctx, err = loadImgFunc(ctx, envCfg)
	Expect(err).NotTo(HaveOccurred())
	loadImgFunc = envfuncs.LoadImageToCluster(kindClusterName, localFakeAgentImage)
	ctx, err = loadImgFunc(ctx, envCfg)
	Expect(err).NotTo(HaveOccurred())

	server = fake_platform_server.NewFakeAzurePipelinesPlatformServer()
	serverPort := serverPortRangeStart + GinkgoParallelProcess()
	err = server.Start(serverPort)
	Expect(err).ToNot(HaveOccurred())

	// Create the namespace here already, so that ktunnel can find it
	createNamespaceFunc := envfuncs.CreateNamespace(controllerNamespace)
	ctx, err = createNamespaceFunc(ctx, envCfg)
	Expect(err).NotTo(HaveOccurred())

	tunnel, err = startK8sTunnel(serverPort)
	Expect(err).ToNot(HaveOccurred())

	// Create the dummy secret for the PAT
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      azpSecretName,
			Namespace: testNamespace,
		},
		StringData: map[string]string{
			"pat": "unused by the fake agent",
		},
	}
	Expect(k8sClient.Create(ctx, secret)).Should(Succeed())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")

	if tunnel != nil {
		err := stopTunnel(tunnel)
		Expect(err).ToNot(HaveOccurred())
	}

	if server != nil {
		err := server.Stop()
		Expect(err).ToNot(HaveOccurred())
	}

	destroyFunc := envfuncs.DestroyCluster(kindClusterName)
	_, err := destroyFunc(ctx, envCfg)
	Expect(err).NotTo(HaveOccurred())
})
