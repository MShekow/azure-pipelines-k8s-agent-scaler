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
	"errors"
	"fmt"
	"github.com/MShekow/azure-pipelines-k8s-agent-scaler/internal/service"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apscalerv1 "github.com/MShekow/azure-pipelines-k8s-agent-scaler/api/v1"
)

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
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create

// Reconcile is called for every change in AutoScaledAgent CRs
func (r *AutoScaledAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var autoScaledAgent apscalerv1.AutoScaledAgent
	if err := r.Get(ctx, req.NamespacedName, &autoScaledAgent); err != nil {
		logger.Error(err, "unable to fetch AutoScaledAgent")
		// Note: client.IgnoreNotFound(err) will convert "err" to "nil" IF the error is of a "not found" type,
		// because the default kube-controller behavior (retrying requests when errors are returned) does not make sense
		// for resources that have already disappeared
		// Note that Kubernetes' deletion propagation feature will automatically make sure that deleting a CR also
		// deletes the Pods or PVCs created for that CR, so we don't need any extra logic here that takes care of that
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	err := r.deleteTerminatedAgentPods(ctx, req, *autoScaledAgent.Spec.MaxTerminatedPodsToKeep)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.terminateFinishedAgentPods(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.deletePromisedAnnotationFromPvcs(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	httpClient := service.CreateHTTPClient()

	azurePat, err := r.getAzurePat(ctx, req, &autoScaledAgent.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}

	poolId, err := service.GetPoolIdFromName(ctx, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("getPoolIdFromName failed")
		return ctrl.Result{}, err
	}

	dummyAgentNames, err := service.CreateOrUpdateDummyAgents(ctx, poolId, azurePat, httpClient, autoScaledAgent.Name, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("CreateOrUpdateDummyAgents() failed")
		return ctrl.Result{}, err
	}
	err = service.DeleteDeadDummyAgents(ctx, poolId, azurePat, httpClient, &autoScaledAgent.Spec, autoScaledAgent.Name, dummyAgentNames)
	if err != nil {
		logger.Info("DeleteDeadDummyAgents() failed")
		return ctrl.Result{}, err
	}

	pendingJobs, err := service.GetPendingJobs(ctx, poolId, azurePat, httpClient, &autoScaledAgent.Spec)
	if err != nil {
		logger.Info("GetPendingJobs() failed")
		return ctrl.Result{}, err
	}
	service.PrintPendingJobsIfChanged(ctx, autoScaledAgent.Name, pendingJobs)

	runningPodsRaw, err := r.getPodsWithPhases(ctx, req, []string{"Running", "Pending"})
	if err != nil {
		logger.Info("getPodsWithPhases(): unable to get Running/Pending pods")
		return ctrl.Result{}, err
	}
	runningPodsFiltered := service.GetFilteredRunningPods(runningPodsRaw)

	runningPods := service.NewRunningPodsWrapper(runningPodsFiltered)

	for _, podsWithCapabilities := range autoScaledAgent.Spec.PodsWithCapabilities {
		matchingPods := runningPods.GetInexactMatch(&podsWithCapabilities.Capabilities)
		matchingPodsCount, matchingPodNames := service.GetPodCountAndNames(matchingPods)
		matchingJobs := pendingJobs.GetInexactMatch(&podsWithCapabilities.Capabilities)
		matchingJobsCount := service.GetJobCount(matchingJobs)

		if int(matchingPodsCount) > service.Min(int(*podsWithCapabilities.MaxCount), service.Max(matchingJobsCount, int(*podsWithCapabilities.MinCount))) {
			// Delete pods because we have too many. This situation should be rare (because agents normally terminate
			// after having run a job), but it can happen in specific situations, e.g.
			// - the user reduced the maxCount in a pod spec in an AutoScaledAgentSpec CR
			// - the user cancelled an AZP job
			// - the AZP job has finished, but the Pod is still running, because the agent container is still working on clean-up tasks (e.g. de-registration)
			logger.Info(fmt.Sprintf("Possibly need to terminate a pod: %d > min(%d, max(%d, %d))", matchingPodsCount, *podsWithCapabilities.MaxCount, len(*matchingJobs), *podsWithCapabilities.MinCount),
				"capabilities", podsWithCapabilities.Capabilities, "matchingPodNames", matchingPodNames)

			agentMinIdleDuration := service.AgentMinIdlePeriodDefault
			if autoScaledAgent.Spec.AgentMinIdlePeriod != nil {
				agentMinIdleDuration = autoScaledAgent.Spec.AgentMinIdlePeriod.Duration
			}

			if terminateablePod, selectionReason, err := r.getTerminateablePod(ctx, matchingPods, agentMinIdleDuration); err != nil {
				logger.Info("unable to get terminateable pod")
				return ctrl.Result{}, err
			} else {
				if terminateablePod != nil {
					err := r.Delete(ctx, terminateablePod, client.PropagationPolicy(metav1.DeletePropagationBackground))
					if err != nil {
						logger.Info("unable to get terminate pod (lower code path)", "podName", terminateablePod.Name)
						return ctrl.Result{}, err
					}
					logger.Info("successfully terminated pod", "podName", terminateablePod.Name, "selectionReason", selectionReason)
				} else {
					logger.Info("did not find any pod that could be terminated")
				}
			}
			continue
		} else if matchingPodsCount < *podsWithCapabilities.MinCount {
			// Start agents (even though there are currently no jobs that need them)
			agentsToCreate := *podsWithCapabilities.MinCount - matchingPodsCount
			if err := r.createAgents(ctx, req, &autoScaledAgent, agentsToCreate, &podsWithCapabilities,
				&podsWithCapabilities.Capabilities); err != nil {
				if exhaustionError, isExhaustionError := err.(*service.PvcExhaustionError); isExhaustionError {
					// Don't return an error to the controller-runtime/framework, because it would be immediately calling
					// Reconcile() again very often, which would not help. Instead, we swallow the error here.
					logger.Info("Reconcile(): failed to create agent that satisfies minCount because of PVC exhaustion", "reusableCacheVolumeName", exhaustionError.ReusableCacheVolumeName)
					continue
				} else {
					logger.Info("failed to create agent that satisfies minCount")
					return ctrl.Result{}, err
				}
			}
			logger.Info("successfully created agents to satisfy minCount", "agentsToCreate", agentsToCreate)
			continue // Do not run the remaining code, to avoid the risk of running conflicting logic in one iteration
		}

		maxAgentsAllowedToCreate := *podsWithCapabilities.MaxCount - matchingPodsCount
		if maxAgentsAllowedToCreate > 0 {
			// agentsWithCapsToCreate contains strings (the concrete capabilities) of agents we want to create in
			// this reconcile cycle.
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

			for i, capabilitiesStr := range agentsWithCapsToCreate {
				if maxAgentsAllowedToCreate == 0 {
					agentsLeftToCreate := len(agentsWithCapsToCreate) - i - 1
					logger.Info("Stopped creating agents to avoid exceeding MaxCount",
						"agentsLeftToCreate", agentsLeftToCreate)
					break
				}

				podCapabilitiesMap := service.GetCapabilitiesMapFromString(capabilitiesStr)
				if err := r.createAgents(ctx, req, &autoScaledAgent, 1, &podsWithCapabilities,
					podCapabilitiesMap); err != nil {
					if exhaustionError, isExhaustionError := err.(*service.PvcExhaustionError); isExhaustionError {
						// Don't return an error to the controller-runtime/framework, because it would be immediately calling
						// Reconcile() again very often, which would not help. Instead, we swallow the error here.
						logger.Info("Reconcile(): unable to create agent for job because of PVC exhaustion",
							"reusableCacheVolumeName", exhaustionError.ReusableCacheVolumeName,
							"podCapabilitiesMap", podCapabilitiesMap)
					} else {
						logger.Info("unable to create agent for job", "podCapabilitiesMap", podCapabilitiesMap)
						return ctrl.Result{}, err
					}
				}
				logger.Info("successfully created agent for job", "podCapabilitiesMap", podCapabilitiesMap)

				maxAgentsAllowedToCreate -= 1
			}
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
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

	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.PersistentVolumeClaim{}, jobOwnerKey, func(rawObj client.Object) []string {
		pvc := rawObj.(*corev1.PersistentVolumeClaim)
		owner := metav1.GetControllerOf(pvc)
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
		//Owns(&corev1.Pod{}).
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
		// but even "chained" selectors use AND (instead of OR), so we need to make a query for each phase in `phases`
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

func (r *AutoScaledAgentReconciler) createAgents(ctx context.Context, req ctrl.Request, agent *apscalerv1.AutoScaledAgent, count int32,
	podsWithCapabilities *apscalerv1.PodsWithCapabilities, capabilities *map[string]string) error {
	logger := log.FromContext(ctx)
	for i := 0; i < int(count); i++ {
		if podName, err := r.createAgent(ctx, req, agent, podsWithCapabilities, capabilities); err != nil {
			return err
		} else {
			logger.Info("createAgents(): successfully created agent pod", "podName", podName, "capabilities", capabilities)
		}
	}

	return nil
}

func (r *AutoScaledAgentReconciler) createAgent(ctx context.Context, req ctrl.Request, agent *apscalerv1.AutoScaledAgent,
	podsWithCapabilities *apscalerv1.PodsWithCapabilities, capabilities *map[string]string) (string, error) {
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
			return "", err
		}
		pod.Spec.Containers = append(pod.Spec.Containers, extraAgentContainerDefs...)
	}

	if err := ctrl.SetControllerReference(agent, pod, r.Scheme); err != nil {
		logger.Error(err, "Unable to set controller reference for pod", "podName", podName,
			"capabilities", capabilitiesStr)
		return "", err
	}

	err := r.assignOrCreatePvcs(ctx, req, agent, pod)
	if err != nil {
		return "", err
	}

	return podName, r.Create(ctx, pod)
}

// assignOrCreatePvcs analyzes the containers of `pod` for volumeMounts
// referencing reusable cache volumes. For any such volumeMount, it adds a volume
// entry to the pod, referencing either an existing un-promised PVC (that can be reused), or creating a new PVC
func (r *AutoScaledAgentReconciler) assignOrCreatePvcs(ctx context.Context, req ctrl.Request, agent *apscalerv1.AutoScaledAgent, pod *corev1.Pod) error {
	logger := log.FromContext(ctx)

	if len(agent.Spec.ReusableCacheVolumes) == 0 {
		return nil
	}

	pvcs := &corev1.PersistentVolumeClaimList{}
	opts := []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		client.MatchingFields{jobOwnerKey: req.Name},
	}
	if err := r.List(ctx, pvcs, opts...); err != nil {
		return err
	}

	// As we don't know how the cached client's results are internally sorted, we sort the PVCs by name alphabetically,
	// because a stable sorting order should improve cache locality (because new pods always use the SAME PVCs)
	sort.Slice(pvcs.Items, func(i, j int) bool {
		return pvcs.Items[i].Name < pvcs.Items[j].Name
	})

	for _, container := range pod.Spec.Containers {
		for _, volumeMount := range container.VolumeMounts {
			if matchingCacheVolume := service.GetMatchingCacheVolume(volumeMount.Name, agent.Spec.ReusableCacheVolumes); matchingCacheVolume != nil {
				var cacheVolumePvc *corev1.PersistentVolumeClaim
				if availablePvc := service.GetUnpromisedPvc(pvcs.Items, volumeMount.Name); availablePvc != nil {
					availablePvc.Annotations[service.ReusableCacheVolumePromisedAnnotationKey] = pod.Name
					err := r.Update(ctx, availablePvc)
					if err != nil {
						logger.Info("assignOrCreatePvcs(): unable to set Promised annotation for cache volume", "volumeName", volumeMount.Name)
						return err
					}
					logger.Info("assignOrCreatePvcs(): using existing PVC for cache volume", "volumeName", volumeMount.Name, "pvcName", availablePvc.Name)
					cacheVolumePvc = availablePvc
				} else {
					if service.IsPvcLimitExceeded(agent, matchingCacheVolume.Name, pvcs.Items) {
						return &service.PvcExhaustionError{ReusableCacheVolumeName: matchingCacheVolume.Name}
					}

					// create a new PVC
					pvcName := fmt.Sprintf("%s-%s", agent.Name, service.GenerateRandomString())

					storageQuantity, err := resource.ParseQuantity(matchingCacheVolume.RequestedStorage)
					if err != nil {
						return err
					}

					volumeMode := corev1.PersistentVolumeFilesystem
					pvc := corev1.PersistentVolumeClaim{
						ObjectMeta: metav1.ObjectMeta{
							Labels: make(map[string]string),
							Annotations: map[string]string{
								service.ReusableCacheVolumePromisedAnnotationKey: pod.Name,
								service.ReusableCacheVolumeNameAnnotationKey:     matchingCacheVolume.Name,
							},
							Name:      pvcName,
							Namespace: agent.Namespace,
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
							Resources: corev1.VolumeResourceRequirements{
								Requests: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceStorage: storageQuantity,
								},
							},
							StorageClassName: &matchingCacheVolume.StorageClassName,
							VolumeMode:       &volumeMode,
						},
					}
					if err := ctrl.SetControllerReference(agent, &pvc, r.Scheme); err != nil {
						logger.Error(err, "Unable to set controller reference for PVC", "pvcName", pvcName)
						return err
					}
					err = r.Create(ctx, &pvc)
					if err != nil {
						logger.Info("assignOrCreatePvcs(): unable to create new PVC for cache volume", "volumeName", volumeMount.Name)
						return err
					}
					logger.Info("assignOrCreatePvcs(): created new PVC for cache volume", "volumeName", volumeMount.Name, "pvcName", pvcName)
					cacheVolumePvc = &pvc
				}

				// Actually use the volume: add new volume to pod.Spec.Volumes
				pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
					Name: volumeMount.Name,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: cacheVolumePvc.Name,
							ReadOnly:  false,
						},
					},
				})
			}
		}
	}

	return nil
}

// deletePromisedAnnotationFromPvcs iterates through reusable cache volume PVCs
// and removes the PromisedFor annotation if the referenced pod either no longer
// exists, or has a terminated phase
func (r *AutoScaledAgentReconciler) deletePromisedAnnotationFromPvcs(ctx context.Context, req ctrl.Request) error {
	logger := log.FromContext(ctx)

	pvcs := &corev1.PersistentVolumeClaimList{}
	opts := []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		client.MatchingFields{jobOwnerKey: req.Name},
	}
	if err := r.List(ctx, pvcs, opts...); err != nil {
		return err
	}

	podList := &corev1.PodList{}
	opts = []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		client.MatchingFields{jobOwnerKey: req.Name},
	}
	if err := r.List(ctx, podList, opts...); err != nil {
		return err
	}

	for _, pvc := range pvcs.Items {
		if promisedPodName, exists := pvc.Annotations[service.ReusableCacheVolumePromisedAnnotationKey]; exists {
			// Find corresponding Pod
			var pod *corev1.Pod
			for _, p := range podList.Items {
				if p.Name == promisedPodName {
					pod = &p
					break
				}
			}

			// Update PVC, removing the "promised" annotation
			// Note: in rare cases pods that were just created in a previous Reconcile() cycle may not be part of
			// <podList>, because of the API client cache (where the Pod would appear just mere sections later), thus
			// HasPodPermanentlyDisappeared() ensures that the pod cannot be found for a while (several seconds),
			// as a workaround, giving the client cache time to catch up
			if (pod == nil && service.HasPodPermanentlyDisappeared(promisedPodName)) || (pod != nil && (pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed)) {
				delete(pvc.Annotations, service.ReusableCacheVolumePromisedAnnotationKey)
				err := r.Update(ctx, &pvc)
				if err != nil {
					logger.Info("deletePromisedAnnotationFromPvcs(): removing promised annotation failed")
					return err
				}

				reason := "pod no longer exists"
				if pod != nil {
					reason = "pod has terminated"
				}
				logger.Info("deletePromisedAnnotationFromPvcs(): removed promised-for annotation",
					"promisedPodName", promisedPodName, "pvcName", pvc.Name, "reason", reason)
			}
		}
	}

	return nil
}

// getTerminateablePod returns the first agent Pod that has either not even been
// scheduled, or where "kubectl exec <podname> pgrep -l Agent.Worker | wc -l"
// returns "0" for the AZP agent container, indicating that the agent in the pod
// is not actively working on any job (and thus safe to terminate). Errors are
// swallowed. Note that for kubectl exec, we need to know the container name, but
// we assume that the first container of the respective podspec is always the
// Azure DevOps Agent container
func (r *AutoScaledAgentReconciler) getTerminateablePod(ctx context.Context,
	pods *map[string][]corev1.Pod, agentMinIdlePeriod time.Duration) (*corev1.Pod, string, error) {
	logger := log.FromContext(ctx)
	for _, podList := range *pods {
		for _, pod := range podList {
			if pod.Status.Phase == corev1.PodPending {
				/*
					We cannot just claim that a Pod whose Phase is "Pending" can be terminated,
					because a Pod stays in Pending until ALL its containers have started. We can,
					however, differentiate between Pods that have not even been scheduled (for those
					the ContainerStatuses are nil).

					For the scheduled Pods (where the ContainerStatuses are not nil) we simply
					assume that the AZP agent container is already running. Kubernetes does NOT
					allow us to RELIABLY determine whether the agent container is up, because the
					ContainerStatuses[*].State.Waiting != nil until _ALL_ containers have
					transitioned to a non-waiting state (the K8s API server does not seem to publish
					"partial" updates for individual containers)!
				*/
				if pod.Status.ContainerStatuses == nil {
					return &pod, "Pod has not yet been scheduled to a node", nil
				}
			}

			agentContainerName := pod.Spec.Containers[0].Name
			cmd := []string{"sh", "-c", "pgrep -l Agent.Worker | wc -l"}
			if stdout, _, err := r.execCommandInPod(ctx, pod.Namespace, pod.Name, agentContainerName, cmd); err == nil {
				if stdout == "0\n" {
					timestampFormat := time.RFC3339
					if _, exists := pod.Annotations[service.IdleAgentPodFirstDetectionTimestampAnnotationKey]; !exists {
						pod.Annotations[service.IdleAgentPodFirstDetectionTimestampAnnotationKey] = time.Now().Format(timestampFormat)
						err = r.Update(ctx, &pod)
						if err != nil {
							// Note that we ignore this error, because it is not critical
							logger.Info("getTerminateablePod(): unable to set the first idle detection timestamp annotation", "podName", pod.Name, "err", err)
						}
					} else {
						// Determine whether the date stored in the IdleAgentPodFirstDetectionTimestampAnnotationKey
						// annotation is older than agentMinIdlePeriod
						firstDetectionTimestampStr := pod.Annotations[service.IdleAgentPodFirstDetectionTimestampAnnotationKey]
						firstDetectionTimestamp, err := time.Parse(timestampFormat, firstDetectionTimestampStr)
						if err != nil {
							// Note that we ignore this error, because it is not critical
							logger.Info("getTerminateablePod(): unable to parse the first idle detection timestamp annotation", "podName", pod.Name, "err", err)
						} else {
							if time.Since(firstDetectionTimestamp) >= agentMinIdlePeriod {
								return &pod, "AZP agent is idle (no Agent.Worker processes)", nil
							} else {
								logger.Info("getTerminateablePod(): AZP agent is idle, but has not been idle for long enough", "podName", pod.Name, "firstDetectionTimestamp", firstDetectionTimestamp)
							}
						}
					}
				} else if stdout == "1\n" {
					logger.Info("getTerminateablePod(): AZP agent container has one active Agent.Worker process", "podName", pod.Name)
					// Possibly delete the first idle detection timestamp annotation, because the agent is no longer idle
					if _, exists := pod.Annotations[service.IdleAgentPodFirstDetectionTimestampAnnotationKey]; exists {
						delete(pod.Annotations, service.IdleAgentPodFirstDetectionTimestampAnnotationKey)
						err = r.Update(ctx, &pod)
						if err != nil {
							// Note that we ignore this error, because it is not critical
							logger.Info("getTerminateablePod(): unable to remove the idle detection timestamp annotation", "podName", pod.Name, "err", err)
						}
					}
				} else {
					logger.Info("getTerminateablePod(): got unexpected stdout of AZP agent container", "podName", pod.Name, "stdout", stdout)
				}
			} else {
				// Note that we ignore this error, because it is not critical
				logger.Info("getTerminateablePod(): unable to exec command in pod", "podName", pod.Name, "err", err)
			}
		}
	}
	return nil, "", nil
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

// terminateFinishedAgentPods terminates agent pods (without deleting them), so
// that one can still access the logs of all the containers in the terminated
// pods (to diagnose problems). Basically, once an Azure Pipelines job has
// completed, the first container (that runs the agent) terminates automatically,
// but if the pod has any other containers defined (statically in the pod spec,
// or by using the ExtraAgentContainers feature), these pods are likely still
// running, and thus the Pod is also still in a "running" phase.
// terminateFinishedAgentPods() terminates those extra containers (which results
// in the Pod phase being "Terminated") by changing the container's image to some
// non-existent image, which makes Kubernetes try to restart the container: the
// container is terminated but not really restarted, because of the image pull
// error. However, the container's logs seem to be preserved.
func (r *AutoScaledAgentReconciler) terminateFinishedAgentPods(ctx context.Context, req ctrl.Request) error {
	logger := log.FromContext(ctx)
	runningPods, err := r.getPodsWithPhases(ctx, req, []string{"Running"})
	if err != nil {
		logger.Error(err, "terminateFinishedAgentPods(): unable to get Running pods")
		return err
	}

	for _, runningPod := range runningPods {
		if len(runningPod.Spec.Containers) > 1 {
			azpAgentContainerStatusIndex, err := service.GetContainerStatusIndex(&runningPod, 0)
			if err != nil {
				return err
			}
			agentContainerHasTerminated := runningPod.Status.ContainerStatuses[azpAgentContainerStatusIndex].State.Terminated != nil
			if agentContainerHasTerminated {
				var containerStatusIndicesToTerminate []int
				for i := 0; i < len(runningPod.Status.ContainerStatuses); i++ {
					if i == azpAgentContainerStatusIndex {
						continue
					}
					if runningPod.Status.ContainerStatuses[i].State.Running != nil {
						containerStatusIndicesToTerminate = append(containerStatusIndicesToTerminate, i)
					}
				}

				if len(containerStatusIndicesToTerminate) > 0 {
					// Terminating a pod takes a while (multiple(!) reconciliation cycles), so to avoid that the reconcile-
					// algorithm is confused by "running" pods that are in reality already terminating, we set our own annotation
					// as a means to filter such pods out
					if _, exists := runningPod.Annotations[service.PodTerminationInProgressAnnotationKey]; !exists {
						logger.Info("terminateFinishedAgentPods(): Found containers to terminate",
							"podName", runningPod.Name, "containerStatusIndicesToTerminate",
							containerStatusIndicesToTerminate)

						for _, containerStatusIndex := range containerStatusIndicesToTerminate {
							containerImage := runningPod.Status.ContainerStatuses[containerStatusIndex].Image
							nonExistentImage := containerImage + service.NonExistentContainerImageSuffix
							specIndex, err := service.GetContainerSpecIndex(&runningPod, containerStatusIndex)
							if err != nil {
								return err
							}
							runningPod.Spec.Containers[specIndex].Image = nonExistentImage
						}

						err = r.Update(ctx, &runningPod)
						if err != nil {
							logger.Error(err, "terminateFinishedAgentPods(): unable to change images to terminate the pod")
							return err
						}

						runningPod.Annotations[service.PodTerminationInProgressAnnotationKey] = "1"
						err = r.Update(ctx, &runningPod)
						if err != nil {
							logger.Error(err, "terminateFinishedAgentPods(): unable to set the termination annotation")
							return err
						}
					}
				}
			}

		}
	}
	return nil
}

func (r *AutoScaledAgentReconciler) execCommandInPod(ctx context.Context, podNamespace, podName, containerName string, command []string) (string, string, error) {
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

	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", "", err
	}

	return stdout.String(), stderr.String(), nil
}
