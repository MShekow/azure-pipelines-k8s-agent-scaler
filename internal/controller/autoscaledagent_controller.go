/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
)

var InMemoryAzurePipelinesPoolIdStore = make(map[string]int64)
var jobOwnerKey = ".metadata.controller"

// AutoScaledAgentReconciler reconciles a AutoScaledAgent object
type AutoScaledAgentReconciler struct {
	client.Client
	RESTClient rest.Interface
	RESTConfig *rest.Config
	Scheme     *runtime.Scheme
}

//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents/finalizers,verbs=update

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create

// Reconcile is called for every change in AutoScaledAgent CRs, or when any of their underlying Pods have changed
func (r *AutoScaledAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var autoScaledAgent apscalerv1.AutoScaledAgent
	if err := r.Get(ctx, req.NamespacedName, &autoScaledAgent); err != nil {
		logger.Error(err, "unable to fetch AutoScaledAgent")
		// Note: client.IgnoreNotFound(err) will convert "err" to "nil" IF the error is of a "not found" type,
		// because the default kube-controller behavior (retrying requests when errors are returned) does not make sense
		// for resources that have already disappeared
		// Note that Kubernetes' deletion propagation feature will automatically make sure that deleting a CR also
		// deletes the Pods created for that CR
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	err := r.deleteTerminatedAgentPods(ctx, req, *autoScaledAgent.Spec.MaxTerminatedPodsToKeep)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.terminatedFinishedAgentPods(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	// TODO stop with error in case there are multiple CRs targeting the same ADP pool

	httpClient := service.CreateHTTPClient()

	azurePat, err := r.getAzurePat(ctx, req, &autoScaledAgent.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}

	poolId, err := getPoolIdFromName(ctx, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("getPoolIdFromName failed")
		return ctrl.Result{}, err
	}

	_, err = service.CreateOrUpdateDummyAgents(ctx, poolId, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("CreateOrUpdateDummyAgents() failed")
		return ctrl.Result{}, err
	}
	// TODO call another method that deletes any offline agent (with name starting
	// with dummy-agent) that has been created some time ago, except for those dummy
	// agents we created in CreateOrUpdateDummyAgents. Also, that method should not
	// be doing work all the time, just e.g. once per hour

	pendingJobs, err := service.GetPendingJobs(ctx, poolId, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("GetPendingJobs() failed")
		return ctrl.Result{}, err
	}

	runningPodsRaw, err := r.getPodsWithPhases(ctx, req, []string{"Running", "Pending"})
	if err != nil {
		logger.Info("getPodsWithPhases(): unable to get Running/Pending pods")
		return ctrl.Result{}, err
	}

	runningPods := service.NewRunningPodsWrapper(runningPodsRaw)

	for _, podsWithCapabilities := range autoScaledAgent.Spec.PodsWithCapabilities {
		matchingPods := runningPods.GetInexactMatch(&podsWithCapabilities.Capabilities)
		matchingPodsCount := service.GetPodCount(matchingPods)

		// Start agents (no matter whether there are jobs that need them or not) if the actual agent pod count
		// is below minCount
		if matchingPodsCount < *podsWithCapabilities.MinCount {
			agentsToCreate := *podsWithCapabilities.MinCount - matchingPodsCount
			if err := r.createAgents(ctx, &autoScaledAgent, agentsToCreate, &podsWithCapabilities,
				&podsWithCapabilities.Capabilities); err != nil {
				logger.Info("failed to create agent because of minCount")
				return ctrl.Result{}, err
			}
			logger.Info("successfully created agents because of minCount", "agentsToCreate", agentsToCreate)
			continue // Do not run the remaining code, to avoid the risk of running conflicting logic in one iteration
		}

		maxAgentsAllowedToCreate := *podsWithCapabilities.MaxCount - matchingPodsCount
		if maxAgentsAllowedToCreate > 0 {
			matchingJobs := pendingJobs.GetInexactMatch(&podsWithCapabilities.Capabilities)
			// Array of strings (the concrete capabilities) of agents we want to create in this reconcile cycle.
			// Note: may contain more entries than we are actually allowed to create
			var agentsWithCapsToCreate []string
			for demandStr, jobs := range *matchingJobs {
				requestedAgentCount := len(jobs)
				actualPodsCount := len((*matchingPods)[demandStr])
				if requestedAgentCount > actualPodsCount {
					agentsToCreate := requestedAgentCount - actualPodsCount
					for i := 0; i < agentsToCreate; i++ {
						agentsWithCapsToCreate = append(agentsWithCapsToCreate, demandStr)
					}
				}
			}
			if len(agentsWithCapsToCreate) == 0 {
				// Delete superfluous pods.
				// Although the number of pods has not exceeded maxCount, we might still have too
				// many pods (more than there are jobs), and thus should kill pods. However, this
				// should be very rare, because agent pods automatically terminate after having completed a job
				// (due to using the "--once" flag)
				for capabilitiesStr, pods := range *matchingPods {
					matchingJobCount := len((*matchingJobs)[capabilitiesStr])
					if len(pods) > service.Max(matchingJobCount, int(*podsWithCapabilities.MinCount)) {
						if terminateablePod, err := r.getTerminateablePod(ctx, matchingPods); err != nil {
							logger.Info("unable to get terminateable pod (upper code path)")
							return ctrl.Result{}, err
						} else {
							if terminateablePod != nil {
								err := r.Delete(ctx, terminateablePod, client.PropagationPolicy(metav1.DeletePropagationBackground))
								if err != nil {
									logger.Info("unable to get terminate pod", "podName", terminateablePod.Name)
									return ctrl.Result{}, err
								}
								logger.Info("successfully terminated pod", "podName", terminateablePod.Name)
							}
						}
					}
				}
			} else {
				for i, capabilitiesStr := range agentsWithCapsToCreate {
					if maxAgentsAllowedToCreate == 0 {
						agentsLeftToCreate := len(agentsWithCapsToCreate) - i - 1
						logger.Info("Stopped creating agents to avoid exceeding MaxCount",
							"agentsLeftToCreate", agentsLeftToCreate)
						break
					}

					podCapabilitiesMap := service.GetCapabilitiesMapFromString(capabilitiesStr)
					if err := r.createAgents(ctx, &autoScaledAgent, 1, &podsWithCapabilities,
						podCapabilitiesMap); err != nil {
						logger.Info("unable to create agent for job", "podCapabilitiesMap", podCapabilitiesMap)
						return ctrl.Result{}, err
					}
					logger.Info("successfully created agent for job", "podCapabilitiesMap", podCapabilitiesMap)

					maxAgentsAllowedToCreate -= 1
				}
			}
		} else if maxAgentsAllowedToCreate < 0 {
			// Delete pods because we have too many. This situation should be rare (because agents normally terminate
			// after having run a job) it can happen in specific situations, e.g. when the user reduced the maxCount
			// in a pod spec in an AutoScaledAgentSpec CR
			if terminateablePod, err := r.getTerminateablePod(ctx, matchingPods); err != nil {
				logger.Info("unable to get terminateable pod (lower code path)")
				return ctrl.Result{}, err
			} else {
				err := r.Delete(ctx, terminateablePod, client.PropagationPolicy(metav1.DeletePropagationBackground))
				if err != nil {
					logger.Info("unable to get terminate pod (lower code path)", "podName", terminateablePod.Name)
					return ctrl.Result{}, err
				}
				logger.Info("successfully terminated pod (lower code path)", "podName", terminateablePod.Name)
			}
		}
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AutoScaledAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, "status.phase", func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		return []string{string(pod.Status.Phase)}
	}); err != nil {
		return err
	}

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, jobOwnerKey, func(rawObj client.Object) []string {
		pod := rawObj.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner == nil {
			return nil
		}
		if owner.APIVersion != apscalerv1.GroupVersion.String() || owner.Kind != "AutoScaledAgent" {
			return nil
		}

		return []string{owner.Name}
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apscalerv1.AutoScaledAgent{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

func (r *AutoScaledAgentReconciler) getAzurePat(ctx context.Context, req ctrl.Request,
	agentSpec *apscalerv1.AutoScaledAgentSpec) (string, error) {
	logger := log.FromContext(ctx)
	var patSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: agentSpec.PersonalAccessTokenSecretName, Namespace: req.Namespace}, &patSecret); err != nil {
		logger.Error(err, "getAzurePat(): unable to fetch Secret", "secretName", agentSpec.PersonalAccessTokenSecretName)
		return "", err
	}

	if pat, ok := patSecret.Data["pat"]; !ok {
		err := errors.New("data key 'pat' is missing")
		logger.Error(err, "getAzurePat(): data key 'pat' is missing in configured secret", "secretName", agentSpec.PersonalAccessTokenSecretName)
		return "", err
	} else {
		return string(pat), nil
	}
}

func (r *AutoScaledAgentReconciler) getPodsWithPhases(ctx context.Context, req ctrl.Request, phases []string) ([]corev1.Pod, error) {
	var allPods []corev1.Pod

	for _, phase := range phases {
		// Note: we use Field selectors (https://kubernetes.io/docs/concepts/overview/working-with-objects/field-selectors/)
		// but even "chained" selectors use AND (instead of OR), so we need to make 2 queries
		podList := &corev1.PodList{}
		opts := []client.ListOption{
			client.InNamespace(req.NamespacedName.Namespace),
			client.MatchingFields{jobOwnerKey: req.Name},
			client.MatchingFields{"status.phase": phase},
		}
		if err := r.List(ctx, podList, opts...); err != nil {
			return nil, err
		}

		for _, pod := range podList.Items {
			// only add Azure Pipelines pods that this controller created
			// (because in the namespace of the CR there might also be other pods, e.g. the controller-manager)
			if _, exists := pod.Annotations[service.CapabilitiesAnnotationName]; exists {
				allPods = append(allPods, pod)
			}
		}
	}

	return allPods, nil
}

func getPoolIdFromName(ctx context.Context, azurePat string, httpClient *http.Client,
	spec *apscalerv1.AutoScaledAgentSpec) (int64, error) {
	// TODO move to another file
	if cachedPoolName, ok := InMemoryAzurePipelinesPoolIdStore[spec.PoolName]; ok {
		return cachedPoolName, nil
	}

	url := fmt.Sprintf("%s/_apis/distributedtask/pools?poolName=%s", spec.OrganizationUrl, spec.PoolName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.SetBasicAuth("", azurePat)
	if err != nil {
		return 0, err
	}

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
		return 0, fmt.Errorf("Azure DevOps REST API returned error. url: %s status: %d response: %s", url, response.StatusCode, string(bytes))
	}

	var result service.AzurePipelinesApiPoolNameResponse
	err = json.Unmarshal(bytes, &result)
	if err != nil {
		return 0, err
	}

	count := len(result.Value)
	if count == 0 {
		return 0, fmt.Errorf("agent pool with name `%s` not found in response", spec.PoolName)
	}

	if count != 1 {
		return 0, fmt.Errorf("found %d agent pools with name `%s`", count, spec.PoolName)
	}

	poolId := int64(result.Value[0].ID)

	InMemoryAzurePipelinesPoolIdStore[spec.PoolName] = poolId

	return poolId, nil
}

func (r *AutoScaledAgentReconciler) createAgents(ctx context.Context, agent *apscalerv1.AutoScaledAgent, count int32,
	podsWithCapabilities *apscalerv1.PodsWithCapabilities, capabilities *map[string]string) error {
	for i := 0; i < int(count); i++ {
		if err := r.createAgent(ctx, agent, podsWithCapabilities, capabilities); err != nil {
			return err
		}
	}

	return nil
}

func (r *AutoScaledAgentReconciler) createAgent(ctx context.Context, agent *apscalerv1.AutoScaledAgent,
	podsWithCapabilities *apscalerv1.PodsWithCapabilities, capabilities *map[string]string) error {
	logger := log.FromContext(ctx)
	podName := fmt.Sprintf("%s-%s", agent.Name, service.GenerateRandomString())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			Name:        podName,
			Namespace:   agent.Namespace,
		},
		Spec: *podsWithCapabilities.PodTemplateSpec.Spec.DeepCopy(),
	}
	for k, v := range podsWithCapabilities.PodAnnotations {
		pod.Annotations[k] = v
	}

	for k, v := range podsWithCapabilities.PodLabels {
		pod.Labels[k] = v
	}

	// Disallow K8s to restart the Pod, just because the agent container finished (with error or successfully)
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever

	capabilitiesStr := service.GetSortedStringificationOfCapabilitiesMap(capabilities)
	pod.Annotations[service.CapabilitiesAnnotationName] = capabilitiesStr

	// Set env vars: AZP_AGENT_NAME, AZP_URL, AZP_POOL and AZP_TOKEN
	azureDevOpsAgentContainer := &pod.Spec.Containers[0]
	azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
		Name:  "AZP_AGENT_NAME",
		Value: podName,
	})
	azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
		Name:  "AZP_URL",
		Value: agent.Spec.OrganizationUrl,
	})
	azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
		Name:  "AZP_POOL",
		Value: agent.Spec.PoolName,
	})
	azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
		Name: "AZP_TOKEN",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: agent.Spec.PersonalAccessTokenSecretName},
				Key:                  "pat",
			},
		},
	})

	for key, val := range *capabilities {
		azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
			Name:  key,
			Value: val,
		})
	}

	if extraAgentContainersStr, exists := (*capabilities)[service.ExtraAgentContainersAnnotationKey]; exists {
		extraAgentContainerDefs, err := service.ParseExtraAgentContainerDefinition(extraAgentContainersStr)
		if err != nil {
			return err
		}
		if len(extraAgentContainerDefs) > 0 {
			pod.Spec.Containers = append(pod.Spec.Containers, extraAgentContainerDefs...)
			azureDevOpsAgentContainer = &pod.Spec.Containers[0]
			azureDevOpsAgentContainer.Env = append(azureDevOpsAgentContainer.Env, corev1.EnvVar{
				Name:  service.ExtraAgentContainersAnnotationKey,
				Value: extraAgentContainersStr,
			})
		}
	}

	if err := ctrl.SetControllerReference(agent, pod, r.Scheme); err != nil {
		logger.Error(err, "Unable to set controller reference for pod", "podName", podName,
			"capabilities", capabilitiesStr)
		return err
	}

	return r.Create(ctx, pod)
}

// getTerminateablePod returns the first Pod object, for which a "kubectl exec
// <podname> pgrep -l Agent.Worker | wc -l" returns "0", indicating that the
// agent in the pod is idle (and thus safe to terminate). Errors are swallowed.
// Note that we need to know the container name, but we assume that the first
// container of the respective podspec is always the Azure DevOps Agent container
func (r *AutoScaledAgentReconciler) getTerminateablePod(ctx context.Context, pods *map[string][]corev1.Pod) (*corev1.Pod, error) {
	//_ := log.FromContext(ctx)
	for _, pods := range *pods {
		for _, pod := range pods {
			agentContainerName := pod.Spec.Containers[0].Name
			cmd := []string{"sh", "-c", "pgrep -l Agent.Worker | wc -l"}
			// TODO differentiate the errors somehow - we only want to "swallow" errors where the pod no longer exists,
			// but still return errors e.g. when K8s RBAC is lacking
			if stdout, _, err := r.execCommandInPod(pod.Namespace, pod.Name, agentContainerName, cmd); err == nil {
				if stdout == "0\n" {
					return &pod, nil
				}
			}
		}
	}
	return nil, nil
}

// deleteTerminatedAgentPods deletes terminated Azure DevOps agent pods, except
// for the <maxPodsToKeep> most recently started pods, which are kept for
// debugging purposes
func (r *AutoScaledAgentReconciler) deleteTerminatedAgentPods(ctx context.Context, req ctrl.Request,
	maxPodsToKeep int32) error {
	logger := log.FromContext(ctx)
	terminatedPods, err := r.getPodsWithPhases(ctx, req, []string{"Succeeded", "Failed"})
	if err != nil {
		logger.Error(err, "deleteTerminatedAgentPods(): unable to get Succeeded/Failed pods")
		return err
	}

	if len(terminatedPods) > int(maxPodsToKeep) {
		sort.Slice(terminatedPods, func(i, j int) bool {
			return terminatedPods[i].Status.StartTime.Time.Before(terminatedPods[j].Status.StartTime.Time)
		})

		for i := 0; i < len(terminatedPods)-int(maxPodsToKeep); i++ {
			terminatedPod := terminatedPods[i]
			err := r.Delete(ctx, &terminatedPod, client.PropagationPolicy(metav1.DeletePropagationBackground))
			if err != nil {
				logger.Error(err, "deleteTerminatedAgentPods(): failed deleting a terminated pod")
				return err
			}
			logger.Info("deleteTerminatedAgentPods(): deleted terminated pod", "podName", terminatedPod.Name)
		}
	}

	return nil
}

// terminatedFinishedAgentPods terminates agent pods (without deleting them), so
// that one can still access the logs of all the containers in the terminated
// pods (to diagnose problems). Basically, once an Azure Pipelines job has
// completed, the first container (that runs the agent) will be terminated, but
// if the pod has any other containers defined (statically in the pod spec, or by
// using the ExtraAgentContainers feature), these pods are likely still running,
// and thus the Pod is also still in a "running" phase.
// terminatedFinishedAgentPods() terminates those extra containers (which results
// in the Pod phase being "Terminated") by changing the container's image to some
// non-existent image, which makes Kubernetes try to restart the container: the
// container is terminated but not really restarted, because of the image pull
// error. However, the container's logs seem to be preserved.
func (r *AutoScaledAgentReconciler) terminatedFinishedAgentPods(ctx context.Context, req ctrl.Request) error {
	logger := log.FromContext(ctx)
	runningPods, err := r.getPodsWithPhases(ctx, req, []string{"Running"})
	if err != nil {
		logger.Error(err, "terminatedFinishedAgentPods(): unable to get Running pods")
		return err
	}

	for _, runningPod := range runningPods {
		if len(runningPod.Spec.Containers) > 1 {
			agentContainerHasTerminated := runningPod.Status.ContainerStatuses[0].State.Terminated != nil
			if agentContainerHasTerminated {
				var containerIndicesToTerminate []int
				for i := 1; i < len(runningPod.Spec.Containers); i++ {
					if runningPod.Status.ContainerStatuses[i].State.Running != nil {
						containerIndicesToTerminate = append(containerIndicesToTerminate, i)
					}
				}

				if len(containerIndicesToTerminate) > 0 {
					logger.Info("Found containers to terminate", "podName", runningPod.Name, "containerIndicesToTerminate", containerIndicesToTerminate)
					for _, containerIndex := range containerIndicesToTerminate {
						containerImage := runningPod.Spec.Containers[containerIndex].Image
						if !strings.Contains(containerImage, service.NonExistentContainerImageSuffix) {
							nonExistentImage := containerImage + service.NonExistentContainerImageSuffix
							runningPod.Spec.Containers[containerIndex].Image = nonExistentImage
							err = r.Update(ctx, &runningPod)
							if err != nil {
								logger.Error(err, "terminatedFinishedAgentPods(): unable to change image", "nonExistentImage", nonExistentImage)
								return err
							}
							logger.Info("updated container image to terminate pod", "containerIndex", containerIndex, "nonExistentImage", nonExistentImage)
						}
					}
				}
			}

		}
	}
	return nil
}

func (r *AutoScaledAgentReconciler) execCommandInPod(podNamespace, podName, containerName string, command []string) (string, string, error) {
	// See https://github.com/kubernetes-sigs/kubebuilder/issues/803 for pointers
	req := r.RESTClient.Post().
		Resource("pods").
		Name(podName).
		Namespace(podNamespace).
		SubResource("exec").
		Param("container", containerName).
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
		}, runtime.NewParameterCodec(r.Scheme))

	var stdout, stderr bytes.Buffer
	exec, err := remotecommand.NewSPDYExecutor(r.RESTConfig, "POST", req.URL())
	if err != nil {
		return "", "", err
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", "", err
	}

	return stdout.String(), stderr.String(), nil
}

//TODO need to document that you need to use "shareProcessNamespace" in the pod
// spec, as otherwise the preStop lifecycle hook
// would not work properly for the OTHER containers. This is only necessary if
// the user defines more than 1 container, either statically (in the pod spec),
// or using ExtraAgentContainers-demands.

//TODO (further in the future): think about whether it makes sense to replace preStop lifecycle hook with dynamically-managed
// PodDisruptionBudget objects. After all, the preStop lifecycle hook only exist to avoid that pods are _voluntarily_
// disrupted (see https://kubernetes.io/docs/concepts/workloads/pods/disruptions/#voluntary-and-involuntary-disruptions),
// e.g. when draining a node. We want to avoid disruption while the agent works on a job, otherwise we don't care
// However, it might happen that the controller is too slow to create a new PodDisruptionBudget object

/*
TODOs: (turn into GitHub Issues)

- Create Helm chart that demonstrates how to set up the CR
- Update documentation
- Simplify reconcile algorithm
- Test normal demands
- Implement reusable cache volumes
- Set up Renovate Bot


*/