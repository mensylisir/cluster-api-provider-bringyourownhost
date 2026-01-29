# Kubeadm 模式对接指南

## ✅ BYOH 自动处理事项

| 组件 | 自动处理 | 说明 |
|------|----------|------|
| **CSR 批准** | ✅ 自动 | ByoAdmissionReconciler 自动批准 CSR |
| **RBAC 权限** | ✅ 自动 | 部署 BYOH Provider 时自动创建 |
| **节点安装** | ✅ 自动 | Agent 执行 K8sInstallerConfig 中的安装脚本 |
| **Bootstrap Token** | ✅ 自动 | Controller 使用 KubeadmConfig 生成 |
| **KubeadmConfig** | ✅ 自动 | Controller 自动创建 bootstrap secret |

## 需要手动处理 ⚠️

实际上，从 v0.5.x 开始，Bootstrap Token 和 KubeadmConfig 都是**自动创建**的，无需手动操作。

只需要创建以下资源：

| 步骤 | 操作 | 说明 |
|------|------|------|
| 1 | 部署 BYOH Provider | `clusterctl init --infrastructure byoh` |
| 2 | 创建 Cluster + ByoCluster | 定义集群 |
| 3 | 创建 ByoMachineTemplate | 引用 KubeadmConfigTemplate |
| 4 | 创建 KubeadmConfigTemplate | CAPI 自动生成 KubeadmConfig |
| 5 | 创建 MachineDeployment | 指定 KubeadmConfigTemplate |
| 6 | 启动 Agent | 自动创建 ByoHost |

---

## 手动创建步骤 (可选)

如果你需要手动创建 Bootstrap Token 和 KubeadmConfig，可以按以下步骤操作：

### 1. 手动创建 Bootstrap Token

```bash
# 生成 token
TOKEN_ID=$(openssl rand -hex 3)
TOKEN_SECRET=$(openssl rand -hex 8)

# 创建 bootstrap token secret
kubectl create secret generic -n kube-system bootstrap-token-${TOKEN_ID} \
  --type=bootstrap.kubernetes.io/token \
  --from-literal=token-id=${TOKEN_ID} \
  --from-literal=token-secret=${TOKEN_SECRET} \
  --from-literal=usage-bootstrap-authentication=true \
  --from-literal=usage-bootstrap-signing=true \
  --from-literal=auth-extra-groups=system:bootstrappers:kubeadm:default-node-token
```

### 2. 手动创建 KubeadmConfig

```yaml
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: KubeadmConfig
metadata:
  name: my-cluster-workers-config
  namespace: default
spec:
  joinConfiguration:
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "site=default"
        cloud-provider: external
```

---

## 推荐：自动模式 (简单)

使用 CAPI 自动生成 Token 和 Config：

```yaml
# kubeadm-mode.yaml
---
# Cluster
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
# ByoCluster
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
# MachineDeployment
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
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: my-cluster-workers-config
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-tmpl
---
# KubeadmConfigTemplate (CAPI 自动创建 KubeadmConfig)
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: KubeadmConfigTemplate
metadata:
  name: my-cluster-workers-config
  namespace: default
spec:
  template:
    spec:
      joinConfiguration:
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "site=default"
            cloud-provider: external
---
# ByoMachineTemplate
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-tmpl
  namespace: default
spec:
  template:
    spec:
      joinMode: kubeadm
---
# K8sInstallerConfigTemplate
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

部署：

```bash
kubectl apply -f kubeadm-mode.yaml
```

节点上启动 Agent：

```bash
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &
```

## 自动发生

| 步骤 | 操作 | 执行者 |
|------|------|--------|
| 1 | 创建 KubeadmConfig | CAPI Controller |
| 2 | 创建 Bootstrap Token | KubeadmConfig Controller |
| 3 | 创建 ByoHost | Agent |
| 4 | CSR 批准 | ByoAdmissionReconciler |

## 节点前置要求

### 1. 安装 Container Runtime

**Ubuntu/Debian:**
```bash
apt-get update
apt-get install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable containerd
systemctl start containerd
```

### 2. 安装 Kubernetes 二进制文件

```bash
K8S_VERSION=v1.34.1
ARCH=amd64

# 下载 kubelet, kubeadm, kubectl
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubelet" -o /usr/local/bin/kubelet
chmod +x /usr/local/bin/kubelet

curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubeadm" -o /usr/local/bin/kubeadm
chmod +x /usr/local/bin/kubeadm

curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubectl" -o /usr/local/bin/kubectl
chmod +x /usr/local/bin/kubectl
```

### 3. 创建必要目录

```bash
mkdir -p /etc/kubernetes/pki
mkdir -p /var/lib/kubelet
mkdir -p /etc/kubernetes/manifests
```

### 4. 下载 BYOH Agent

```bash
AGENT_VERSION=v0.5.86
wget -O /usr/local/bin/byoh-hostagent \
  "https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/${AGENT_VERSION}/byoh-hostagent-linux-amd64"
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

# 查看 CSR 状态
kubectl get csr
```

## 故障排查

### 问题 1: kubelet 无法启动

```bash
# 检查 kubelet 状态
systemctl status kubelet

# 检查日志
journalctl -u kubelet --no-pager -n 100

# 常见原因：cgroup driver 不匹配
grep -r "SystemdCgroup" /etc/containerd/config.toml
```

### 问题 2: CSR 未被批准

```bash
# 查看 CSR
kubectl get csr

# 手动批准
kubectl certificate approve <csr-name>
```

### 问题 3: 缩容后 Node 对象残留

**v0.5.80+ 已修复**。

```bash
# 手动删除
kubectl delete node <node-name>
```

### 问题 4: kube-proxy 无权限读取 nodes

**v0.5.83+ 已修复**。

```bash
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.86/config/rbac/byohost_kube_proxy_clusterrole.yaml
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.86/config/rbac/byohost_kube_proxy_clusterrolebinding.yaml
```

## 快速命令

```bash
# 1. 部署 Provider
clusterctl init --infrastructure byoh

# 2. 创建资源
kubectl apply -f kubeadm-mode.yaml

# 3. 节点上启动 Agent
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &
```
