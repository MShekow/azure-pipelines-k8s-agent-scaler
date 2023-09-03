## Usage

[Helm](https://helm.sh) must be installed to use the charts.  Please refer to
Helm's [documentation](https://helm.sh/docs) to get started.

Once Helm has been set up correctly, add the repo as follows:

`helm repo add <alias> https://mshekow.github.io/azure-pipelines-k8s-agent-scaler`

If you had already added this repo earlier, run `helm repo update` to retrieve the latest versions of the packages.  You can then run `helm search repo <alias>` to see the charts.

To install/upgrade the `azp-k8s-agent-scaler-operator` chart:

`helm upgrade --install --namespace azp-operator --create-namespace <your-release-name> <alias>/azp-k8s-agent-scaler-operator`

You can exchange the namespace (`azp-operator`) for any other namespace.

To uninstall the chart: `helm uninstall <your-release-name>`
