# Multi-Pool Architecture for BYOH

This document describes how to configure multiple node pools with different purposes (e.g., worker nodes, GPU nodes) using BYOH with Cluster Autoscaler integration.

## Overview

BYOH supports multi-pool architecture through label-based host selection. Each node pool can target specific BYOHosts based on labels, enabling:
- Heterogeneous clusters (mix of CPU/GPU nodes)
- Workload isolation (different node pools for different teams)
- Resource optimization (specialized hardware for specialized workloads)

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                         BYOHost Capacity Pool                        │
├─────────────────────┬─────────────────────┬─────────────────────────┤
│   node10            │   node11            │   node12                │
│   labels:           │   labels:           │   labels:               │
│   purpose=worker    │   purpose=gpu       │   purpose=general       │
│   gpu=false         │   gpu=true          │   gpu=false             │
└─────────┬───────────┴──────────┬──────────┴────────────┬────────────┘
          │                      │                       │
          ▼                      ▼                       ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────────┐
│ ByoMachineTemplate  │ │ ByoMachineTemplate  │ │ ByoMachineTemplate      │
│ (worker-template)   │ │ (gpu-template)      │ │ (general-template)      │
│ selector:           │ │ selector:           │ │ selector:               │
│   purpose=worker    │ │   purpose=gpu       │ │   purpose=general       │
└──────────┬──────────┘ └──────────┬──────────┘ └────────────┬────────────┘
           │                       │                        │
           ▼                       ▼                        ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────────┐
│ MachineDeployment   │ │ MachineDeployment   │ │ MachineDeployment       │
│ (worker-pool)       │ │ (gpu-pool)          │ │ (general-pool)          │
│ replicas: 2         │ │ replicas: 1         │ │ replicas: 3             │
└─────────────────────┘ └─────────────────────┘ └─────────────────────────┘
```

## Label Selection Mechanism

### How It Works

1. **Cluster Autoscaler** monitors MachineDeployments via annotations
2. **MachineSet** creates Machines when scale-up is triggered
3. **ByoMachine** uses `spec.selector` from ByoMachineTemplate
4. **BYOH Controller** matches selector against BYOHost labels

```
Cluster Autoscaler → MachineDeployment (annotations) 
    ↓
MachineSet → Machine
    ↓
ByoMachine (selector: purpose=gpu)
    ↓
BYOH Controller: Match BYOHost.labels.purpose == "gpu"
    ↓
Selected BYOHost is assigned to the Machine
```

### Key Labels

| Label | Source | Purpose |
|-------|--------|---------|
| `byohost.infrastructure.cluster.x-k8s.io/host` | Manual | Host name identifier |
| `capacity.infrastructure.cluster.x-k8s.io/cpu` | Auto-detected | CPU capacity |
| `capacity.infrastructure.cluster.x-k8s.io/memory` | Auto-detected | Memory capacity |
| `purpose` | Manual | **Custom label for node pool selection** |
| `gpu` | Manual | GPU availability flag |

## Configuration Steps

### Step 1: Label BYOHosts

Assign labels to your BYOHosts based on their capabilities:

```bash
# Worker nodes (no GPU)
kubectl label byohost node10 -n default purpose=worker --overwrite
kubectl label byohost node12 -n default purpose=worker --overwrite

# GPU nodes
kubectl label byohost node11 -n default purpose=gpu gpu=true --overwrite

# General purpose nodes
kubectl label byohost node13 -n default purpose=general --overwrite
```

### Step 2: Create ByoMachineTemplates

Create a ByoMachineTemplate for each node pool:

#### Worker Pool Template
```bash
kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: worker-template
  namespace: default
spec:
  template:
    spec:
      selector:
        matchLabels:
          purpose: worker
      bootstrapConfigRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: BootstrapKubeconfig
        name: my-cluster-workers-bootstrap-kubeconfig
      installerRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: K8sInstallerConfigTemplate
        name: my-cluster-worker-installer
      joinMode: tlsBootstrap
EOF
```

#### GPU Pool Template
```bash
kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: gpu-template
  namespace: default
spec:
  template:
    spec:
      selector:
        matchLabels:
          purpose: gpu
      bootstrapConfigRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: BootstrapKubeconfig
        name: my-cluster-workers-bootstrap-kubeconfig
      installerRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: K8sInstallerConfigTemplate
        name: my-cluster-worker-installer
      joinMode: tlsBootstrap
EOF
```

### Step 3: Create MachineDeployments

Create separate MachineDeployments for each node pool:

#### Worker Pool
```bash
kubectl apply -f - <<EOF
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: worker-pool
  namespace: default
  labels:
    cluster-autoscaler-enabled: "true"
spec:
  clusterName: my-cluster
  replicas: 2
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    metadata:
      labels:
        cluster-autoscaler-enabled: "true"
        cluster.x-k8s.io/cluster-name: my-cluster
        purpose: worker
    spec:
      clusterName: my-cluster
      version: v1.34.1
      bootstrap:
        configRef:
          apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
          kind: BootstrapKubeconfig
          name: my-cluster-workers-bootstrap-kubeconfig
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: worker-template
EOF
```

#### GPU Pool
```bash
kubectl apply -f - <<EOF
apiVersion: cluster.x-k8s.io/v1beta2
kind: MachineDeployment
metadata:
  name: gpu-pool
  namespace: default
  labels:
    cluster-autoscaler-enabled: "true"
spec:
  clusterName: my-cluster
  replicas: 1
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    metadata:
      labels:
        cluster-autoscaler-enabled: "true"
        cluster.x-k8s.io/cluster-name: my-cluster
        purpose: gpu
    spec:
      clusterName: my-cluster
      version: v1.34.1
      bootstrap:
        configRef:
          apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
          kind: BootstrapKubeconfig
          name: my-cluster-workers-bootstrap-kubeconfig
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: gpu-template
EOF
```

### Step 4: Configure Autoscaler Annotations

Add autoscaler annotations to each MachineDeployment:

```bash
# Worker pool
kubectl annotate machinedeployment worker-pool -n default \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size=0 \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size=5

# GPU pool
kubectl annotate machinedeployment gpu-pool -n default \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size=0 \
  cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size=2
```

### Step 5: Deploy Cluster Autoscaler

Ensure the Cluster Autoscaler is configured to discover both node pools:

```bash
kubectl get pods -n kube-system | grep cluster-autoscaler
```

The autoscaler will automatically discover MachineDeployments with the `cluster-autoscaler-enabled: true` label.

## Pod Scheduling

### Using nodeSelector

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  nodeSelector:
    purpose: gpu  # Schedule on GPU nodes
  containers:
  - name: gpu-container
    image: nvidia/cuda:11.0
    resources:
      limits:
        nvidia.com/gpu: 1
```

### Using Tolerations

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-workload
spec:
  nodeSelector:
    purpose: gpu
  tolerations:
  - key: "nvidia.com/gpu"
    operator: "Exists"
    effect: "NoSchedule"
  containers:
  - name: gpu-container
    image: nvidia/cuda:11.0
    resources:
      limits:
        nvidia.com/gpu: 1
```

## Verification Commands

### Check BYOHost Labels
```bash
kubectl get byohost -n default --show-labels
```

### Check ByoMachineTemplate Selector
```bash
kubectl get byomachinetemplate <template-name> -n default -o jsonpath='{.spec.template.spec.selector}'
```

### Check MachineDeployment Status
```bash
kubectl get machinedeployment -n default -o wide
```

### Check Node Labels
```bash
kubectl get nodes --show-labels
```

### Monitor Autoscaler Logs
```bash
kubectl logs -n kube-system <autoscaler-pod> --tail=50 | grep -i "discovered\|nodegroup"
```

## Common Issues

### Issue: Nodes Not Scaling Up

**Symptom:** MachineDeployment replicas increase but no new nodes appear.

**Diagnosis:**
```bash
# Check BYOHost availability
kubectl get byohost -n default

# Check BYOHost labels match selector
kubectl get byohost <hostname> -n default -o jsonpath='{.metadata.labels}'

# Check ByoMachineTemplate selector
kubectl get byomachinetemplate <template> -n default -o jsonpath='{.spec.template.spec.selector}'
```

**Solution:** Ensure BYOHost labels match the ByoMachineTemplate selector.

### Issue: Wrong BYOHost Selected

**Symptom:** GPU node assigned to worker pool or vice versa.

**Diagnosis:**
```bash
# Check selector in ByoMachineTemplate
kubectl get byomachinetemplate <template> -n default -o yaml | grep -A 10 selector

# Check BYOHost labels
kubectl get byohost -n default --show-labels
```

**Solution:** Update BYOHost labels or ByoMachineTemplate selector to match correctly.

## Best Practices

1. **Use Consistent Label Names:** Establish a naming convention for labels (e.g., `purpose`, `team`, `environment`).

2. **Document Labels:** Maintain documentation of label meanings for your cluster.

3. **Test Before Production:** Verify label matching in a non-production environment first.

4. **Monitor Autoscaler:** Regularly check autoscaler logs to ensure correct node pool selection.

5. **Separate Templates:** Create separate ByoMachineTemplates for each node pool type.

6. **Configure Autoscaler Bounds:** Set appropriate min/max replicas for each node pool based on expected load.

## Related Documentation

- [Cluster Autoscaler Integration](autoscaler.md)
- [Autoscaler Troubleshooting](autoscaler_troubleshooting.md)
- [BYOH Agent Configuration](byoh_agent.md)
