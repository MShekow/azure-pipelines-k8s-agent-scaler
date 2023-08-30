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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
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
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=azurepipelines.k8s.scaler.io,resources=autoscaledagents/finalizers,verbs=update

//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// TODO add permissions for kubectl exec
//   - apiGroups: [""]
//    resources: ["pods/exec"]
//    verbs: ["create"]

// Reconcile is called for every change in AutoScaledAgent CRs, or when any of their underlying Pods have changed
func (r *AutoScaledAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var autoScaledAgent apscalerv1.AutoScaledAgent
	if err := r.Get(ctx, req.NamespacedName, &autoScaledAgent); err != nil {
		logger.Error(err, "unable to fetch AutoScaledAgent")
		// Note: client.IgnoreNotFound(err) will convert "err" to "nil" IF the error is of a "not found" type,
		// because the default kubecontroller behavior (retrying requests when errors are returned) does not make sense
		// for resources that have already disappeared
		// TODO: we might want to NOT return here already, but still do some clean-up work
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO: delete terminated pods that the controller owns

	// TODO stop with error in case there are multiple CRs targeting the same ADP pool

	httpClient := service.CreateHTTPClient()

	azurePat, err := r.getAzurePat(ctx, req, &autoScaledAgent.Spec)

	poolId, err := getPoolIdFromName(ctx, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}

	fmt.Printf("Poolname %d", poolId)

	pendingJobs, err := service.GetPendingJobs(ctx, poolId, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}

	runningPodsRaw, err := r.getRunningPods(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	runningPods := service.NewRunningPodsContainer(runningPodsRaw)

	for _, podsWithCapabilities := range autoScaledAgent.Spec.PodsWithCapabilities {
		matchingPods := runningPods.GetInexactMatch(&podsWithCapabilities.Capabilities)
		matchingPodsCount := service.GetPodCount(matchingPods)

		// Start agents (no matter whether there are jobs that need them or not) if the actual agent pod count
		// is below minCount
		if matchingPodsCount < *podsWithCapabilities.MinCount {
			agentsToCreate := *podsWithCapabilities.MinCount - matchingPodsCount
			if err := r.createAgents(ctx, &autoScaledAgent, agentsToCreate, &podsWithCapabilities.PodTemplateSpec,
				&podsWithCapabilities.Capabilities); err != nil {
				return ctrl.Result{}, err
			}
			continue
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
				// should be very rare, because agent pods terminate after having completed a job
				for capabilitiesStr, pods := range *matchingPods {
					matchingJobCount := len((*matchingJobs)[capabilitiesStr])
					if len(pods) > matchingJobCount {
						if terminateablePod, err := r.getTerminateablePod(ctx, matchingPods); err != nil {
							return ctrl.Result{}, err
						} else {
							err := r.Delete(ctx, terminateablePod, client.PropagationPolicy(metav1.DeletePropagationBackground))
							if err != nil {
								return ctrl.Result{}, err
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
					if err := r.createAgents(ctx, &autoScaledAgent, 1, &podsWithCapabilities.PodTemplateSpec,
						podCapabilitiesMap); err != nil {
						return ctrl.Result{}, err
					}

					maxAgentsAllowedToCreate -= 1
				}
			}
		} else if maxAgentsAllowedToCreate < 0 {
			// Delete pods because we have too many. This situation should be rare (because agents normally terminate
			// after having run a job) it can happen in specific situations, e.g. when the user reduced the maxCount
			// in a pod spec in an AutoScaledAgentSpec CR
			if terminateablePod, err := r.getTerminateablePod(ctx, matchingPods); err != nil {
				return ctrl.Result{}, err
			} else {
				err := r.Delete(ctx, terminateablePod, client.PropagationPolicy(metav1.DeletePropagationBackground))
				if err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AutoScaledAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apscalerv1.AutoScaledAgent{}).
		Complete(r)
}

func (r *AutoScaledAgentReconciler) getAzurePat(ctx context.Context, req ctrl.Request,
	agentSpec *apscalerv1.AutoScaledAgentSpec) (string, error) {
	logger := log.FromContext(ctx)
	var patSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: agentSpec.PersonalAccessTokenSecretName, Namespace: req.Namespace}, &patSecret); err != nil {
		logger.Error(err, "unable to fetch Secret", "name", agentSpec.PersonalAccessTokenSecretName)
		return "", err
	}

	if pat, ok := patSecret.Data["pat"]; !ok {
		err := errors.New("data key 'pat' is missing")
		logger.Error(err, "data key 'pat' is missing")
		return "", err
	} else {
		return string(pat), nil
	}
}

func (r *AutoScaledAgentReconciler) getRunningPods(ctx context.Context, req ctrl.Request) ([]corev1.Pod, error) {
	// Note: we use Field selectors (https://kubernetes.io/docs/concepts/overview/working-with-objects/field-selectors/)
	// and even "chained" selectors use AND (instead of OR), so we need to make 2 queries
	runningPodList := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		client.MatchingFields{jobOwnerKey: req.Name},
		client.MatchingFields{"status.phase": "Running"},
	}

	if err := r.List(ctx, runningPodList, opts...); err != nil {
		return nil, err
	}

	pendingPodList := &corev1.PodList{}
	optsPending := []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		client.MatchingFields{jobOwnerKey: req.Name},
		client.MatchingFields{"status.phase": "Pending"},
	}

	if err := r.List(ctx, pendingPodList, optsPending...); err != nil {
		return nil, err
	}

	allPods := append(runningPodList.Items, pendingPodList.Items...)
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
	podTemplateSpec *corev1.PodTemplateSpec, capabilities *map[string]string) error {
	for i := 0; i < int(count); i++ {
		if err := r.createAgent(ctx, agent, podTemplateSpec, capabilities); err != nil {
			return err
		}
	}

	return nil
}

func (r *AutoScaledAgentReconciler) createAgent(ctx context.Context, agent *apscalerv1.AutoScaledAgent,
	podTemplateSpec *corev1.PodTemplateSpec, capabilities *map[string]string) error {
	logger := log.FromContext(ctx)
	podName := fmt.Sprintf("%s-%d", agent.Name, service.GenerateRandomString())

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			Name:        podName,
			Namespace:   agent.Namespace,
		},
		Spec: *podTemplateSpec.Spec.DeepCopy(),
	}
	for k, v := range podTemplateSpec.Annotations {
		pod.Annotations[k] = v
	}

	for k, v := range podTemplateSpec.Labels {
		pod.Labels[k] = v
	}

	capabilitiesStr := service.GetSortedStringificationOfCapabilitiesMap(capabilities)
	pod.Annotations[service.CapabilitiesAnnotationName] = capabilitiesStr

	if extraAgentContainers, exists := (*capabilities)[service.ExtraAgentContainersAnnotationKey]; exists {
		extraAgentContainerDefs, err := service.ParseExtraAgentContainerDefinition(extraAgentContainers)
		if err != nil {
			return err
		}
		if len(extraAgentContainerDefs) > 0 {
			pod.Spec.Containers = append(pod.Spec.Containers, extraAgentContainerDefs...)
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
// <podname> pgrep -l Agent.Worker | wc -l" returns 0 lines, indicating that the
// agent in the pod is idle (and thus safe to terminate). Errors are swallowed.
// Note that we need to know the container name, but we assume that the first
// container of the respective podspec is always the Azure DevOps Agent container
func (r *AutoScaledAgentReconciler) getTerminateablePod(ctx context.Context, pods *map[string][]corev1.Pod) (*corev1.Pod, error) {
	//_ := log.FromContext(ctx) TODO implement, see https://github.com/kubernetes-sigs/kubebuilder/issues/803
	return nil, nil
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
