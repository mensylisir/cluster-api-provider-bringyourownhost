# Cluster Autoscaler Troubleshooting for BYOH

This document provides troubleshooting guidance for common issues when integrating Cluster Autoscaler with the BringYourOwnHost (BYOH) provider.

## Table of Contents

- [Common Symptoms](#common-symptoms)
- [Diagnostic Commands](#diagnostic-commands)
- [Known Issues and Solutions](#known-issues-and-solutions)
  - [Annotation Key Mismatch](#annotation-key-mismatch)
  - [BootstrapKubeconfig Missing dataSecretCreated](#bootstrapkubeconfig-missing-datasecretcreated)
  - [Machine Controller Cache Issues](#machine-controller-cache-issues)
  - [BYOHost Stuck in Cleanup Loop](#byohost-stuck-in-cleanup-loop)
- [Scale-Up Flow Verification](#scale-up-flow-verification)

---

## Common Symptoms

| Symptom | Description |
|---------|-------------|
| `nodegroup has no scaling capacity, skipping` | Autoscaler cannot find a valid node group to scale |
| Pods stay `Pending` indefinitely | No nodes available to schedule pods |
| MachineDeployment replicas stay at 0 | Autoscaler triggered but no Machines created |
| `BootstrapConfigReady: false` | BootstrapKubeconfig status not properly set |
| BYOHost conditions oscillating | Host conditions flip between Ready and not Ready |

---

## Diagnostic Commands

### Check MachineDeployment Annotations
```bash
kubectl get machinedeployment <name> -n <namespace> -o jsonpath='{.metadata.annotations}'
```

Expected annotations for autoscaler:
```
cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "0"
cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "2"
```

### Check Autoscaler Logs
```bash
# Find autoscaler pod
kubectl get pods -n kube-system | grep cluster-autoscaler

# View logs
kubectl logs -n kube-system <autoscaler-pod-name> --tail=100 | grep -i "my-cluster\|nodegroup\|scale\|capacity\|unschedulable"
```

### Check BootstrapKubeconfig Status
```bash
kubectl get bootstrapkubeconfig <name> -n <namespace> -o yaml | grep -A 10 "status:"
```

Expected status:
```yaml
status:
  dataSecretName: bootstrap-token-xxxxx
  initialization:
    dataSecretCreated: true
```

### Check Machine Conditions
```bash
kubectl get machine <name> -n <namespace> -o jsonpath='{range .status.conditions[*]}{.type}: {.reason} = {.status}{"\n"}{end}'
```

---

## Known Issues and Solutions

### Annotation Key Mismatch

**Symptom:**
```
I0130 xx:xx:xx.xxx  clusterapi_controller.go:770] discovered node group: MachineDeployment/default/my-cluster-workers (min: 0, max: 2, replicas: 0)
# But autoscaler never triggers scale-up
```

**Root Cause:**
The autoscaler expects specific annotation keys. BYOH was using a non-standard key.

**Incorrect (does not work):**
```bash
# These annotations are WRONG
cluster.x-k8s.io/cluster-autoscaler-min-size
cluster.x-k8s.io/cluster-autoscaler-max-size
```

**Correct:**
```bash
# These annotations are CORRECT
cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size
cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size
```

**Fix:**
```bash
kubectl patch machinedeployment <name> -n <namespace> --type='json' -p='[
  {"op": "remove", "path": "/metadata/annotations/cluster.x-k8s.io~1cluster-autoscaler-min-size"},
  {"op": "remove", "path": "/metadata/annotations/cluster.x-k8s.io~1cluster-autoscaler-max-size"},
  {"op": "add", "path": "/metadata/annotations/cluster.x-k8s.io~1cluster-api-autoscaler-node-group-min-size", "value": "0"},
  {"op": "add", "path": "/metadata/annotations/cluster.x-k8s.io~1cluster-api-autoscaler-node-group-max-size", "value": "2"}
]'
```

**Verification:**
```bash
# After fix, autoscaler logs should show:
# I0130 xx:xx:xx.xxx  clusterapi_controller.go:770] discovered node group: MachineDeployment/default/my-cluster-workers (min: 0, max: 2, replicas: 0)
# And when scaling up:
# I0130 xx:xx:xx.xxx  orchestrator.go:185] Best option to resize: MachineDeployment/default/my-cluster-workers
# I0130 xx:xx:xx.xxx  orchestrator.go:261] Final scale-up plan: [{MachineDeployment/default/my-cluster-workers 0->1 (max: 2)}]
```

---

### BootstrapKubeconfig Missing dataSecretCreated

**Symptom:**
```
BootstrapConfigReady: NotReady = False
Message: "BootstrapKubeconfig status.initialization.dataSecretCreated is false"
```

**Root Cause:**
When a `BootstrapKubeconfig` is cloned (e.g., for a new Machine), the cloned resource may not have `dataSecretName` and `dataSecretCreated` set properly. This prevents the Machine controller from recognizing that the bootstrap data is ready.

**Fix:**
Ensure the `BootstrapKubeconfig` controller sets these fields when cloning:

```go
// In controllers/infrastructure/bootstrapkubeconfig_controller.go
if bootstrapKubeconfig.Spec.BootstrapKubeconfigData != nil {
    // Set dataSecretName for cloned resources
    dataSecretName := "bootstrap-token-" + generateToken()
    bootstrapKubeconfig.Status.DataSecretName = &dataSecretName
    
    // Set dataSecretCreated to true
    initialization.DataSecretCreated = true
}
```

**Verification:**
```bash
kubectl get bootstrapkubeconfig <name> -n <namespace> -o jsonpath='{.status.dataSecretCreated}'
# Should return: true
```

---

### Machine Controller Cache Issues

**Symptom:**
- `BootstrapConfigReady` stays `false` even after `BootstrapKubeconfig` has `dataSecretCreated: true`
- Machine conditions don't update after fixing the underlying issue

**Root Cause:**
The CAPI Machine controller caches resource states. After fixing a `BootstrapKubeconfig`, the Machine may still see the old cached status.

**Fix Options:**

1. **Trigger Re-reconcile by annotating the Machine:**
   ```bash
   kubectl annotate machine <name> -n <namespace> --overwrite last-reconcile-time=$(date +%s)
   ```

2. **Restart the CAPI controller manager:**
   ```bash
   kubectl rollout restart deployment/capi-controller-manager -n capi-system
   kubectl rollout status deployment/capi-controller-manager -n capi-system --timeout=60s
   ```

3. **Delete the Machine and let MachineSet recreate it:**
   ```bash
   kubectl delete machine <name> -n <namespace>
   ```

**Verification:**
```bash
kubectl get machine <name> -n <namespace> -o jsonpath='{.status.observedGeneration}'
# Should match spec generation: kubectl get machine <name> -n <namespace> -o jsonpath='{.metadata.generation}'
```

---

### BYOHost Stuck in Cleanup Loop

**Symptom:**
```
K8sNodeBootstrapSucceeded: True → False → True → False (oscillating)
```

**Root Cause:**
The BYOH agent detects `K8sNodeBootstrapSucceeded=True`, initiates cleanup, which sets the condition to `False`. On the next reconcile, the condition is set back to `True` because the MachineRef is still valid, creating an infinite loop.

**Fix:**
In `agent/reconciler/host_reconciler.go`, when a host has no `MachineRef` (idle host), simply return and wait rather than setting conditions:

```go
// Before (causes infinite loop):
if host.Status.MachineRef == nil {
    conditions.MarkFalse(host, K8sNodeBootstrapSucceeded, ...)
    return ctrl.Result{}, nil
}

// After (correct behavior):
if host.Status.MachineRef == nil {
    // Idle host - just wait for a Machine to be assigned
    return ctrl.Result{}, nil
}
```

**Verification:**
```bash
# Check BYOHost conditions no longer oscillate
kubectl get byohost <name> -n <namespace> -o yaml | grep -A 20 "conditions:"
```

---

## Scale-Up Flow Verification

Use this checklist to verify the complete scale-up flow:

### 1. Create Unschedulable Pod
```bash
# Create a pod that can only run on a specific BYOHost
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-scaleup-pod
  namespace: default
spec:
  nodeSelector:
    byohost.infrastructure.cluster.x-k8s.io/host: <hostname>
  containers:
  - name: test
    image: nginx:alpine
    resources:
      requests:
        cpu: "100m"
        memory: "64Mi"
EOF
```

**Expected:** Pod should be `Pending` (unschedulable)

### 2. Verify Autoscaler Detects Unschedulable Pod
```bash
kubectl logs -n kube-system <autoscaler-pod> --tail=50 | grep -i "unschedulable\|Pod.*is unschedulable"
```

**Expected:**
```
Pod default/test-scaleup-pod is unschedulable
```

### 3. Verify Autoscaler Plans Scale-Up
```bash
kubectl logs -n kube-system <autoscaler-pod> --tail=50 | grep -i "scale-up\|resize\|my-cluster-workers"
```

**Expected:**
```
Best option to resize: MachineDeployment/default/my-cluster-workers
Final scale-up plan: [{MachineDeployment/default/my-cluster-workers 0->1 (max: 2)}]
Scale-up: setting group MachineDeployment/default/my-cluster-workers size to 1 instead of 0 (max: 2)
```

### 4. Verify MachineDeployment Scales
```bash
kubectl get machinedeployment <name> -n <namespace>
```

**Expected:**
```
my-cluster-workers   my-cluster   False       1         1         0       0           1            Running   xx   v1.34.1
```

### 5. Verify Machine and ByoMachine Created
```bash
kubectl get machine,byomachine,bootstrapkubeconfig -n <namespace>
```

**Expected:**
- Machine created with `BootstrapConfigReady: Ready`
- ByoMachine created with `BYOHostReady: Ready`
- BootstrapKubeconfig has `dataSecretCreated: true`

### 6. Verify Pod Gets Scheduled
```bash
kubectl get pod test-scaleup-pod -n <namespace>
```

**Expected:**
```
NAME               READY   STATUS    RESTARTS   AGE
test-scaleup-pod   1/1     Running   0          xx
```

---

## Additional Resources

- [Cluster Autoscaler Documentation](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
- [BYOH Autoscaler Setup Guide](autoscaler.md)
- [CAPI Machine Controller](https://cluster-api.sigs.k8s.io/developer/architecture/controllers/machine)
