## 📋 需要创建的完整资源清单

是的！需要创建以下资源：

```
┌─────────────────────────────────────────────────────────────────┐
│ 用户手动创建                                                    │
├─────────────────────────────────────────────────────────────────┤
│ 1. Cluster                                                     │
│ 2. ByoCluster                                                  │
│ 3. ByoMachineTemplate (MachineDeployment 引用它)                │
│ 4. MachineDeployment (CAPI 会自动创建 Machine)                  │
└─────────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────────┐
│ CAPI 自动创建                                                   │
├─────────────────────────────────────────────────────────────────┤
│ 5. Machine (根据 MachineDeployment 自动创建)                    │
└─────────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────────┐
│ BYOH 控制器自动创建                                             │
├─────────────────────────────────────────────────────────────────┤
│ 6. ByoMachine (根据 Machine 自动创建)                           │
└─────────────────────────────────────────────────────────────────┘
```



## 📝 完整 YAML 配置文件

创建一个文件 `cluster-setup.yaml`：

```
---
# 1. ByoMachineTemplate - 定义新节点的配置
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: md-0
  namespace: default
spec:
  template:
    spec:
      # TLS Bootstrap推荐 模式（，用于已有集群）
      joinMode: tlsBootstrap
      # K8s 版本
      kubernetesVersion: v1.28.0
      # 下载模式：online 从网络下载，offline 用本地 bundle
      downloadMode: online
---
# 2. MachineDeployment - 定义要扩容的节点
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: md-0
  namespace: default
spec:
  clusterName: my-cluster
  replicas: 1  # 要添加几个节点就填几
  selector:
    matchLabels: null
  template:
    metadata:
      labels:
        nodepool: pool1
    spec:
      clusterName: my-cluster
      version: v1.28.0  # K8s 版本
      # 引用 ByoMachineTemplate
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: md-0
        namespace: default
---
# 3. Cluster - 定义集群（必须先创建 Cluster 和 ByoCluster）
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  # 网络配置
  clusterNetwork:
    pods:
      cidrBlocks:
      - 192.168.0.0/16
    services:
      cidrBlocks:
      - 10.96.0.0/12
  # 控制平面端点（你的现有集群 API Server）
  controlPlaneEndpoint:
    host: 192.168.1.100  # 替换为你的 API Server IP
    port: 6443
  # 关键！都指向 ByoCluster
  controlPlaneRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ByoCluster
    name: my-cluster
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ByoCluster
    name: my-cluster
---
# 4. ByoCluster - BYOH 特有的集群定义
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: 192.168.1.100  # 替换为你的 API Server IP
    port: 6443
  # 可选：Bundle 仓库地址
  # bundleLookupBaseRegistry: projects.registry.vmware.com/byoh
```



## 🚀 部署步骤

```
# 1. 先创建 Cluster 和 ByoCluster
kubectl apply -f cluster-setup.yaml -l "kind in (Cluster,ByoCluster)"

# 2. 检查状态
kubectl get cluster,byocluster

# 3. 确认 Cluster 状态变为 ready
kubectl get cluster my-cluster -o jsonpath='{.status.infrastructureReady}'
kubectl get cluster my-cluster -o jsonpath='{.status.controlPlaneReady}'

# 4. 然后创建 MachineDeployment 和 ByoMachineTemplate
kubectl apply -f cluster-setup.yaml -l "kind in (MachineDeployment,ByoMachineTemplate)"

# 5. 检查 MachineDeployment
kubectl get machinedeployment

# 6. 检查 Machine 是否自动创建
kubectl get machine

# 7. 检查 ByoMachine 是否自动创建
kubectl get byomachine
```



## 📊 创建顺序（重要！）

```
第一步：kubectl apply -f cluster-setup.yaml  # 包含 Cluster 和 ByoCluster
         ↓
第二步：确认 Cluster 状态
         kubectl get cluster my-cluster
         # 期望看到 infrastructureReady=true, controlPlaneReady=true
         ↓
第三步：kubectl apply -f cluster-setup.yaml  # 包含 MachineDeployment 和 ByoMachineTemplate
         ↓
第四步：检查自动创建的资源
         kubectl get machine,byomachine,byohost
```



## 🔍 验证命令

```
# 查看所有资源
kubectl get cluster,byocluster,machinedeployment,machine,byomachine,byohost

# 查看 Machine 状态
kubectl get machine -o wide

# 查看 ByoMachine 状态和条件
kubectl get byomachine -o yaml | grep -A 20 "status:"

# 查看 BYOH 控制器日志
kubectl logs -n byoh-system -l control-plane=controller-manager -f
```



------

## ⚠️ 重要提醒

1. **Cluster 和 ByoCluster 必须同名**（都是 `my-cluster`）
2. **MachineDeployment.spec.clusterName 必须和 Cluster 同名**
3. **先创建 Cluster/ByoCluster，确认它们状态 ready 后再创建 MachineDeployment**
4. **ByoHost 需要先注册好**（在你的新节点上启动 agent）

明天到公司后，先执行 `kubectl get cluster,byocluster,machinedeployment,machine,byomachine,byohost` 把结果发给我！