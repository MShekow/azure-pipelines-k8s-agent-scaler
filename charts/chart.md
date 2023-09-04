Scratchpad:

- one folder, with a CRD-sub-chart, referenced by the main chart, see ECK chart
    - CRD chart: azurepipelines.k8s.scaler.io_autoscaledagents.yaml
    - main chart:
        - manager.yaml
        - service_account.yaml
        - role.yaml
        - role_binding.yaml
        - leader_election_role.yaml
        - leader_election_role_binding.yaml
        - Need to check which _labels_ we need to write down in the files explicitly,
          see https://helm.sh/docs/chart_best_practices/labels/
    - Another folder with a simple Helm chart that demonstrates how to run an Agent. Includes the RBAC-hack for the AZP
      agent
