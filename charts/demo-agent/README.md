# Demo agent Helm chart

This chart demonstrates how to set up Azure Pipeline Agents (for one specific agent pool) that are capable of running
jobs with multiple containers.

Just make a copy of this folder and modify the chart you see fit.

To install/upgrade the chart (example):

`helm upgrade --install --namespace azp --create-namespace <your-release-name> demo-agent`

See also the `azure-pipelines.yaml` for a demonstration of how a CI/CD pipeline could look.
