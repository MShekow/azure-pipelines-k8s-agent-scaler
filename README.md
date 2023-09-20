# azure-pipelines-k8s-agent-scaler

A Kubernetes operator that provisions ephemeral _Pods_ that run Azure DevOps Pipelines agents, as well as other sidecar
containers.

This operator is written in Go, based on [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime). We
use [kubebuilder](https://book.kubebuilder.io/) for bootstrapping. **This solution is completely _unrelated_ to KEDA.**

## Background: why create yet another solution?

As of 2023, Azure Pipelines has the following methods for _self-hosting_ elastically-scalable agents:

1. [Azure Virtual Machine Scale Set agents](https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/scale-set-agents?view=azure-devops),
   which are _slowly_ auto-scaling VMs (several minutes of provisioning time), where you get poor resource usage (due to
   having only one agent per VM)
2. [KEDA](https://keda.sh/docs/latest/scalers/azure-pipelines/) (a general-purpose Kubernetes operator) using its Azure
   Pipelines scaler, or meta-level solutions based on it,
   e.g. [Azure Pipelines Agent](https://github.com/clemlesne/azure-pipelines-agent/)
3. Kubernetes operators built specifically for Azure Pipelines agents, such
   as https://github.com/ogmaresca/azp-agent-autoscaler/ or https://github.com/microsoft/azure-pipelines-orchestrator,
   which have all been discontinued

While the KEDA-based solution is the most economical one, it has many technical drawbacks:

- With KEDA, running agents whose pods have _multiple_ containers (e.g. because you need the tools contained in the
  respective images) is cumbersome. Instead of using Azure Pipeline's _demands / capabilities_ feature, you have to
  create a dedicated _agent pool_ for each set of containers, and maintain a correspondingly large set of KEDA-specific
  YAML manifests
- It is not easily possible to run agents with containers _dynamically-defined_ in your pipeline YAML file.
  Example: job #1 builds and pushes a Docker image (with a tag that depends on an Azure Pipelines variable) that you
  want to run with a KEDA-based agent in job #2 that starts after job #1). The only solution is to start a dynamic
  container as an _ephemeral_ container (in an already-running agent Pod), which has many other drawbacks (e.g. an
  ephemeral container cannot be protected from termination via a `preStop` lifecycle hook, it is invisible in most
  tools, and its _resource_ usage is not accounted for via `requests`/`limits`)
- For every agent pool for which you configure KEDA, you need to define at least one agent `Job`/`Deployment`
  with `minReplicaCount` larger than 0, as otherwise your jobs would not even start. This disallows you to use the "
  scale to zero" approach, unless you _manually_ take care of registering a fake/dummy agent for each pool/demand
  yourself
- If you use _long-running_ agent pods (i.e., _not_ providing the `--once` flag to the Azure Pipelines agent
  container), KEDA may prematurely kill your agent pods, resulting in aborted pipelines and many 'offline' agents in
  your agent pool. Why? Because KEDA scales your
  Deployments/Jobs only based on the _number_ of pending jobs. Suppose two jobs are pending, and two Deployments are
  scheduled by KEDA. One job terminates quickly, the other one takes longer. The pending job count gets reduced from 2
  to 1, and KEDA down-scales the Deployments, and Kubernetes may (arbitrarily) try to kill the one that is still runs
  the active job.
    - While you could just use _ephemeral_ pods (with the `--once` flag), e.g. as Kubernetes _Jobs_, as done
      in https://github.com/clemlesne/azure-pipelines-agent, their disadvantage is that they lack support for _cache
      volumes_: Kubernetes has no mechanism to ensure that a cache volume is only concurrently used by _one_ Job (the "
      Once" in the `ReadWriteOnce` access mode is highly misleading)

For these reasons, this Kubernetes operator provides a better solution solving all of the above problems at once.

## Description

`azure-pipelines-k8s-agent-scaler` (this project) manages Kubernetes _Pods_ that run the Azure Pipelines (AZP) agent
Docker
image (see [here](https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/docker?view=azure-devops#linux)).
The pods are _ephemeral_, meaning that the agent container is started with the `--once` flag, such that it terminates
after having completed _one_ Azure Pipelines job.

Features of `azure-pipelines-k8s-agent-scaler`:

- Ability to specify multiple pod configurations, each one for a different set of Azure Pipeline _capabilities_. For
  each pod, you can configure a min/max count for the pods, and define several _sidecar containers_, e.g. BuildKit, or
  any other tools you need in your pipeline
- Automatic _termination_ of agent pods: once the AZP agent container has terminated, `azure-pipelines-k8s-agent-scaler`
  will automatically stop all other sidecar containers, to transition the pod into a terminated state
- Automatic _deletion_ of terminated pods (with the configurable ability to keep the N most recently terminated pods,
  for debugging purposes)
- Careful termination of superfluous agent pods (which only happens under rare circumstances anyway): only those pods
  are killed that are currently not running any AZP job
- Configurable definition of _cache volumes_ that are mounted to the defined pods (e.g. to speed up BuildKit via a local
  cache). `azure-pipelines-k8s-agent-scaler` provisions new volumes if necessary, and re-mounts existing volumes to new
  pods, ensuring that a volume is mounted to only _one_ pod
- Ability to specify extra containers (including their CPU and memory limits) right in the AZP pipeline YAML file via
  _demands_ (example:
  `ExtraAgentContainers -equals containername,someImage:someTag,250m,64Mi||otherContainerName,someOtherImage:someTag,500m,128Mi`).
  Note that the values can also be _dynamic_, e.g. by populating the demand with AZP _variables_
- Automatic registration of (offline) dummy/fake AZP agents that have the _demands_ that you defined in your
  configuration. This is necessary because the AZP platform would otherwise abort jobs that have demands for which there
  are no _registered_ agents. This AZP-platform-behavior conflicts with the _dynamic_ registration of agents, as done by
  the `azure-pipelines-k8s-agent-scaler` operator, but it can be worked around via the automatic pre-registration of
  agents. Note: if you define `ExtraAgentContainers` you can fully-dynamically register new agents using the companion
  CLI tool [azure-pipelines-agent-registrator](https://github.com/MShekow/azure-pipelines-agent-registrator)
- Operator can be installed into your Kubernetes cluster via **Kustomize** or **Helm**

## Getting Started

You need a Kubernetes cluster to run against, e.g. the one by Docker Desktop, [k3d](https://k3d.io),
[KIND](https://sigs.k8s.io/kind), or a remote cluster.

**Note:** Your controller automatically uses the current context in your `kubeconfig` file (i.e. whatever
cluster `kubectl cluster-info` shows).

### Running on the cluster

For **local development**, it is recommended to use the **Kustomize** manifests stored in the `config/default` folder.

Since this project is based on kubebuilder, it comes with various Make targets (check the [Makefile](Makefile) for
details).

Examples:

- `make install` installs only the CRD (`make uninstall` removes the CRD again)
- `make docker-build` builds the local Docker image that contains the controller-manager (by default tagged
  as `controller:latest`)
- `make deploy` installs the Docker-based version into your cluster (installing the CRD, controller-manager `Deployment`
  and various RBAC-related manifests). You need to _manually_ run `make docker-build` beforehand!
- `make undeploy` reverts the effects of `make deploy`

For a **production** deployment, it is recommended to use
the [Helm chart](https://mshekow.github.io/azure-pipelines-k8s-agent-scaler/) instead.

Next, create a dedicated Kubernetes _namespace_ that hosts your AZP agent `Pods`. Inside it, create a `Secret` that
contains your AZP Personal Access Token:

`kubectl create secret generic azure-pat --from-literal=pat=YOUR-PAT-HERE --namespace <your-namespace>`

Finally, create your desired `CustomResource` (see the [sample](sample/v1_autoscaledagent.yaml)) and apply it to the
cluster (to `<your-namespace>`), or use the [demo-agent Helm chart](charts/demo-agent).

## Contributing

// TODO(user): Add detailed information on how you would like others to contribute to this project

### How it works

This project aims to follow the
Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the
cluster.

### Test It Out

1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make run
```

**NOTE:** You can also run this in one step by running: `make install run`

### Modifying the API definitions

If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)
