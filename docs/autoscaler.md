# Cluster Autoscaler Integration for BYOH

This document describes how to configure and deploy the Kubernetes Cluster Autoscaler to work with the BringYourOwnHost (BYOH) provider.

## Overview

The Cluster Autoscaler automatically adjusts the size of the Kubernetes cluster when:
- There are pods that failed to run in the cluster due to insufficient resources.
- There are nodes in the cluster that have been underutilized for an extended period of time and their pods can be placed on other existing nodes.

For BYOH, this means automatically claiming available `ByoHost` resources when scale-up is needed, and releasing them back to the pool when scale-down occurs.

## Setup Guide

### 1. Install BYOH Provider
Ensure the BYOH provider is installed in your **Management Cluster**.

```bash
clusterctl init --infrastructure byoh
```

### 2. Register Capacity Pool (Available Hosts)
The Autoscaler needs a pool of idle hosts to claim when scaling up. These hosts must be registered with the Management Cluster but not yet assigned to a Machine.

For each spare host:
1.  **Generate Bootstrap Kubeconfig** (on Management Cluster):
    ```bash
    # Get API Server endpoint
    APISERVER=$(kubectl config view -ojsonpath='{.clusters[0].cluster.server}')
    CA_CERT=$(kubectl config view --flatten -ojsonpath='{.clusters[0].cluster.certificate-authority-data}')

    # Create BootstrapKubeconfig CR
    cat <<EOF | kubectl apply -f -
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: BootstrapKubeconfig
    metadata:
      name: bootstrap-kubeconfig
      namespace: default
    spec:
      apiserver: "$APISERVER"
      certificate-authority-data: "$CA_CERT"
    EOF

    # Export config to file
    kubectl get bootstrapkubeconfig bootstrap-kubeconfig -n default -o=jsonpath='{.status.bootstrapKubeconfigData}' > bootstrap-kubeconfig.conf
    ```

2.  **Run Host Agent** (on the Spare Host):
    Download the agent binary and run it with the bootstrap config.
    ```bash
    # Download agent (replace version as needed)
    wget https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/v0.5.0/byoh-hostagent-linux-amd64
    chmod +x byoh-hostagent-linux-amd64
    
    # Start agent
    ./byoh-hostagent-linux-amd64 --bootstrap-kubeconfig bootstrap-kubeconfig.conf
    ```

3.  **Verify Registration**:
    ```bash
    kubectl get byohosts
    # NAME     STATUS   AGE
    # host-1   Ready    2m
    # host-2   Ready    2m
    ```
    *Note: Ensure these hosts are `Ready` and do not have a `MachineRef` yet.*

### 3. Prepare Workload Cluster
Ensure you have a workload cluster created. The Autoscaler will monitor this cluster's pod pending state.

## Configuration

### 1. ProviderID

The key to Autoscaler integration is the `ProviderID`. 
- **Agent**: The BYOH Agent automatically configures the `kubelet` on each node with `--provider-id=byoh://<hostname>`.
- **Controller**: The BYOH Controller automatically syncs this ID to the `ByoMachine` object.

Ensure your nodes show the correct ProviderID:
```bash
kubectl get nodes -o custom-columns=NAME:.metadata.name,PROVIDER_ID:.spec.providerID
```

### 2. Cluster Autoscaler Deployment

You need to deploy the Cluster Autoscaler in your **Management Cluster**.

#### RBAC

The Autoscaler needs permissions to modify `MachineSet` and `Machine` objects.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  labels:
    k8s-app: cluster-autoscaler
  name: cluster-autoscaler
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-autoscaler
  labels:
    k8s-app: cluster-autoscaler
rules:
  - apiGroups: [""]
    resources: ["events", "endpoints"]
    verbs: ["create", "patch"]
  - apiGroups: [""]
    resources: ["pods/eviction"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["pods/status"]
    verbs: ["update"]
  - apiGroups: [""]
    resources: ["endpoints"]
    resourceNames: ["cluster-autoscaler"]
    verbs: ["get", "update"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["watch", "list", "get", "update"]
  - apiGroups: [""]
    resources: ["namespaces", "pods", "services", "replicationcontrollers", "persistentvolumeclaims", "persistentvolumes"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["batch"]
    resources: ["jobs", "cronjobs"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["apps"]
    resources: ["daemonsets", "replicasets", "statefulsets"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "csinodes", "csidrivers", "csistoragecapacities"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["create"]
  - apiGroups: ["coordination.k8s.io"]
    resourceNames: ["cluster-autoscaler"]
    resources: ["leases"]
    verbs: ["get", "update"]
  # CAPI Permissions
  - apiGroups: ["cluster.x-k8s.io"]
    resources: ["machines", "machinesets", "machineclasses", "machinedeployments"]
    verbs: ["watch", "list", "get", "update"]
  - apiGroups: ["infrastructure.cluster.x-k8s.io"]
    resources: ["byohosts", "byomachines"]
    verbs: ["watch", "list", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-autoscaler
  labels:
    k8s-app: cluster-autoscaler
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-autoscaler
subjects:
  - kind: ServiceAccount
    name: cluster-autoscaler
    namespace: kube-system
```

#### Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cluster-autoscaler
  namespace: kube-system
  labels:
    app: cluster-autoscaler
spec:
  selector:
    matchLabels:
      app: cluster-autoscaler
  replicas: 1
  template:
    metadata:
      labels:
        app: cluster-autoscaler
    spec:
      serviceAccountName: cluster-autoscaler
      containers:
        - image: registry.k8s.io/autoscaling/cluster-autoscaler:v1.24.0
          name: cluster-autoscaler
          command:
            - ./cluster-autoscaler
            - --cloud-provider=clusterapi
            - --namespace=default
            - --max-nodes-total=10
            - --v=4
```

## GPU Support & Heterogeneous Clusters

BYOH supports **Capacity Awareness** to help the Cluster Autoscaler make better scaling decisions, especially in heterogeneous environments (mix of small/large nodes, CPU/GPU nodes).

### 1. Auto-Discovery
The BYOH Host Agent automatically detects the host's capacity and reports it to the Management Cluster:
- **CPU/Memory**: Automatically detected.
- **GPU**: NVIDIA GPUs are detected via `lspci`.

### 2. Labels
The Agent automatically applies labels to the `ByoHost` object:
- `capacity.infrastructure.cluster.x-k8s.io/cpu`: e.g., "8"
- `capacity.infrastructure.cluster.x-k8s.io/memory`: e.g., "32Gi"
- `capacity.infrastructure.cluster.x-k8s.io/gpu`: e.g., "1" (if GPU exists)
- `nvidia.com/gpu.count`: e.g., "1" (for easier selection)

### 3. Usage with Autoscaler
To ensure the Autoscaler picks the right node (e.g., a GPU node for a GPU workload):
1.  **Tag your ByoHosts**: Ensure your hosts have the correct labels (done automatically by Agent).
2.  **Use MachineDeployment with Selector**:
    Create a `MachineDeployment` that selects only GPU hosts.

    ```yaml
    apiVersion: cluster.x-k8s.io/v1beta1
    kind: MachineDeployment
    metadata:
      name: gpu-pool
    spec:
      template:
        spec:
          infrastructureRef:
            kind: ByoMachineTemplate
            name: gpu-template
    ---
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ByoMachineTemplate
    metadata:
      name: gpu-template
    spec:
      template:
        spec:
          selector:
            matchLabels:
              nvidia.com/gpu.count: "1"
    ```

When a Pod requests `nvidia.com/gpu: 1`, the Autoscaler will see that the `gpu-pool` MachineDeployment can satisfy this request and will scale it up, which in turn will claim a ByoHost with the `nvidia.com/gpu.count: 1` label.

## Troubleshooting

If you encounter issues with autoscaler integration, refer to the [Troubleshooting Guide](autoscaler_troubleshooting.md) for detailed diagnostics and solutions to common problems including:

- Annotation key mismatches
- BootstrapKubeconfig status issues
- Machine controller cache problems
- BYOHost cleanup loops
