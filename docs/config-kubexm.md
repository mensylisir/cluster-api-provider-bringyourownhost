# Kubexm (TLS Bootstrap) 模式对接指南

## 概述

Kubexm 模式使用二进制 kubelet，通过 TLS Bootstrap 加入集群。Agent 负责安装和管理 kubelet。

## 工作流程

```
1. 部署 BYOH Provider
2. 创建 Cluster + ByoCluster + ByoMachineTemplate + K8sInstallerConfigTemplate
3. 创建 MachineDeployment
4. 节点上启动 Agent → 自动创建 ByoHost → 安装 kubelet → 加入集群
```

## 部署步骤

### 1. 部署 BYOH Provider

```bash
clusterctl init --infrastructure byoh
```

### 2. 创建资源文件

```yaml
# scale-up-existing-cluster.yaml
---
# Cluster: 定义集群网络
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  clusterNetwork:
    pods:
      cidrBlocks: ["172.20.0.0/16"]
    services:
      cidrBlocks: ["10.68.0.0/16"]
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ByoCluster
    name: my-cluster
---
# ByoCluster: 指向现有 API Server
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: "172.30.1.18"
    port: 6443
---
# MachineDeployment: 定义 Worker 节点
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: my-cluster-workers
  namespace: default
spec:
  clusterName: my-cluster
  replicas: 1
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    spec:
      clusterName: my-cluster
      version: v1.34.1
      bootstrap:
        dataSecretName: my-cluster-workers-bootstrap-kubeconfig
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-tmpl
        namespace: default
---
# ByoMachineTemplate: 定义安装模板
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-tmpl
  namespace: default
spec:
  template:
    spec:
      joinMode: tlsBootstrap
      bootstrapConfigRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: BootstrapKubeconfig
        name: my-cluster-workers-bootstrap-kubeconfig
      installerRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: K8sInstallerConfigTemplate
        name: my-cluster-worker-installer
---
# K8sInstallerConfigTemplate: 定义安装包
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: K8sInstallerConfigTemplate
metadata:
  name: my-cluster-worker-installer
  namespace: default
spec:
  template:
    spec:
      bundleType: k8s
      bundleRepo: projects.registry.vmware.com/cluster-api-provider-bringyourownhost
```

### 3. 应用配置

```bash
kubectl apply -f scale-up-existing-cluster.yaml
```

### 4. 节点上启动 Agent

```bash
# 获取集群 kubeconfig
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf

# 启动 Agent（自动创建 ByoHost）
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &
```

## 自动发生

| 步骤 | 操作 | 执行者 |
|------|------|--------|
| 1 | 创建 BootstrapKubeconfig（包含 token） | Controller |
| 2 | 创建 ByoHost（包含节点信息） | Agent |
| 3 | 分配 ByoHost 给 Machine | Controller |
| 4 | 安装 kubelet | Agent |
| 5 | CSR 批准 | Controller |

## 节点前置要求

在节点上需要提前安装：

```bash
# 1. containerd
apt-get install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd

# 2. kubelet 二进制
K8S_VERSION=v1.34.1
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/amd64/kubelet" -o /usr/local/bin/kubelet
chmod +x /usr/local/bin/kubelet

# 3. BYOH Agent
wget -O /usr/local/bin/byoh-hostagent \
  "https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/v0.5.86/byoh-hostagent-linux-amd64"
chmod +x /usr/local/bin/byoh-hostagent
```

## 验证

```bash
# 查看 Machine 状态
kubectl get machines -n default

# 查看 ByoHost（自动创建）
kubectl get byohosts -n default

# 查看节点
kubectl get nodes
```

## 故障排查

### 问题 1: 缩容后 Node 对象残留

**v0.5.80+ 已修复**。Agent 现在会在清理时自动删除 Node 对象。

```bash
# 手动删除残留 Node
kubectl delete node <node-name>
```

### 问题 2: kube-proxy 无权限读取 nodes

**v0.5.83+ 已修复**。部署 RBAC：

```bash
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.86/config/rbac/byohost_kube_proxy_clusterrole.yaml
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.86/config/rbac/byohost_kube_proxy_clusterrolebinding.yaml
```

### 问题 3: kubelet CSR 未被批准

```bash
# 查看 CSR
kubectl get csr

# 手动批准
kubectl certificate approve <csr-name>
```

## 快速命令

```bash
# 1. 部署 Provider
clusterctl init --infrastructure byoh

# 2. 创建资源
kubectl apply -f scale-up-existing-cluster.yaml

# 3. 节点上启动 Agent
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &
```
