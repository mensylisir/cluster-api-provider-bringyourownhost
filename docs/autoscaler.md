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
    wget https://github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/releases/download/v0.5.0/byoh-hostagent-linux-amd64
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
  - apiGroups: ["cluster.x-k8s.io"]
    resources: ["machines", "machinesets", "machineemployments"]
    verbs: ["get", "list", "watch", "update", "patch"]
  - apiGroups: ["infrastructure.cluster.x-k8s.io"]
    resources: ["byomachines", "byohosts"]
    verbs: ["get", "list", "watch"]
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
    resources: ["pods", "services", "replicationcontrollers", "persistentvolumeclaims", "persistentvolumes"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["extensions"]
    resources: ["replicasets", "daemonsets"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["apps"]
    resources: ["statefulsets", "replicasets", "daemonsets"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses", "csinodes", "csidrivers", "csistoragecapacities"]
    verbs: ["watch", "list", "get"]
  - apiGroups: ["batch", "extensions"]
    resources: ["jobs"]
    verbs: ["get", "list", "watch", "patch"]
  - apiGroups: ["coordination.k8s.io"]
    resources: ["leases"]
    verbs: ["create"]
  - apiGroups: ["coordination.k8s.io"]
    resourceNames: ["cluster-autoscaler"]
    resources: ["leases"]
    verbs: ["get", "update"]
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

Use the `clusterapi` cloud provider implementation.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cluster-autoscaler
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: cluster-autoscaler
  template:
    metadata:
      labels:
        app: cluster-autoscaler
    spec:
      serviceAccountName: cluster-autoscaler
      containers:
        - image: registry.k8s.io/autoscaling/cluster-autoscaler:v1.27.0
          name: cluster-autoscaler
          command:
            - ./cluster-autoscaler
            - --cloud-provider=clusterapi
            - --namespace=default  # Namespace of your Workload Cluster resources
            - --auto-discovery-cluster-api=true
            - --v=4
```

### 3. Enabling Auto-Scaling on MachineSet

To enable auto-scaling for a specific `MachineSet`, add the following annotations:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineSet
metadata:
  name: my-cluster-worker
  annotations:
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "10"
spec:
  clusterName: my-cluster
  replicas: 1
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
      cluster.x-k8s.io/deployment-name: my-cluster-worker
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
        cluster.x-k8s.io/deployment-name: my-cluster-worker
    spec:
      version: v1.27.3
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: my-cluster-worker
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker
```

## How it Works

1.  **Scale Up**:
    - When unschedulable pods appear, Autoscaler increases the `replicas` count of the `MachineSet`.
    - The CAPI controller creates a new `Machine`.
    - The BYOH controller claims an available `ByoHost` for the new `Machine`.
    - The Agent on the host detects the claim and installs Kubernetes.
    - The Node joins the cluster with `ProviderID=byoh://<hostname>`.
    - Autoscaler sees the new node and schedules pods.

2.  **Scale Down**:
    - When a node is underutilized, Autoscaler decreases the `MachineSet` replicas.
    - The CAPI controller deletes the `Machine`.
    - The BYOH controller triggers the "Uninstall" script on the host.
    - The host is reset (kubeadm reset, cleanup) and released back to the pool.
    - The `ByoHost` becomes available for future claims.

## Troubleshooting

- **Node not registering**: Check Agent logs for `kubeadm join` errors.
- **Autoscaler not scaling**: Check Autoscaler logs for "failed to find node group for node". This usually means `ProviderID` mismatch.
