# Kubexm (TLS Bootstrap) 模式对接指南

## ✅ BYOH 自动处理事项

| 组件 | 自动处理 | 说明 |
|------|----------|------|
| **ByoHost** | ✅ 自动 | Agent 启动时自动创建 |
| **BootstrapKubeconfig** | ✅ 自动 | Controller 自动生成，包含 token |
| **CSR 批准** | ✅ 自动 | ByoAdmissionReconciler 自动批准 |
| **RBAC 权限** | ✅ 自动 | 部署 BYOH Provider 时自动创建 |

## 只需要手动处理 ⚠️

| 步骤 | 操作 | 说明 |
|------|------|------|
| 1 | 部署 BYOH Provider | `clusterctl init --infrastructure byoh` |
| 2 | 创建 4 个资源 | Cluster, ByoCluster, ByoMachineTemplate, K8sInstallerConfigTemplate |
| 3 | 创建 MachineDeployment | 指定 replicas 扩容节点 |
| 4 | 部署 Agent | 在节点上启动 byoh-hostagent |

---

## 自动创建 Token 配置

### 方式 1: 指定 bootstrapConfigRef (推荐)

在 ByoMachineTemplate 中指定 `bootstrapConfigRef`，Controller 会自动创建 BootstrapKubeconfig 和对应的 token：

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec:
      joinMode: tlsBootstrap
      # 指定 bootstrapConfigRef，Controller 会自动创建 BootstrapKubeconfig
      bootstrapConfigRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: BootstrapKubeconfig
        name: my-cluster-bootstrap-kubeconfig
---
# BootstrapKubeconfig (不需要手动创建，Controller 会自动创建)
# 如果不存在，Controller 会自动创建包含 token 的 Secret
```

### 方式 2: 不指定 bootstrapConfigRef (Controller 自动生成)

如果 `bootstrapConfigRef` 为空，Controller 会自动从本地集群生成 bootstrap kubeconfig：

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec:
      joinMode: tlsBootstrap
      # 不指定 bootstrapConfigRef，Controller 自动生成
```

---

## 最小配置示例

### 步骤 1: 部署 BYOH Provider

```bash
clusterctl init --infrastructure byoh
```

### 步骤 2: 创建 MachineDeployment (最简配置)

```yaml
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
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
    spec:
      clusterName: my-cluster
      version: v1.34.1
      bootstrap:
        # 不需要 configRef，Controller 自动生成 bootstrap secret
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-template
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec:
      joinMode: tlsBootstrap  # 必须指定
```

### 步骤 3: 创建 ByoHost (注册节点)

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHost
metadata:
  name: node10
  namespace: default
spec:
  # 不需要指定 bootstrapSecret，Controller 自动处理
```

### 步骤 4: 启动 Agent (在节点上执行)

```bash
# 获取集群 kubeconfig
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf

# 启动 Agent
nohup byoh-hostagent \
  --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf \
  > /var/log/byoh-agent.log 2>&1 &
```

---

## 完整配置示例 (包含可选配置)

### Cluster + ByoCluster

```yaml
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
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: "172.30.1.18"
    port: 6443
```

### ByoMachineTemplate

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec:
      joinMode: tlsBootstrap
      # 可选：指定 bootstrapSecret（如果不指定，Controller 自动生成）
      # bootstrapSecret:
      #   name: my-custom-bootstrap-secret
      #   namespace: default
```

### ByoHost

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHost
metadata:
  name: node10
  namespace: default
  labels:
    site: default
spec:
  # 可选：管理 kube-proxy
  manageKubeProxy: false
  # 可选：节点标签
  labels:
    site: default
  # 可选：污点
  taints: []
  # 可选：指定 bootstrapSecret
  # bootstrapSecret:
  #   name: my-custom-bootstrap-secret
  #   namespace: default
```

### MachineDeployment

```yaml
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
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
    spec:
      clusterName: my-cluster
      version: v1.34.1
      # 不需要 bootstrap.configRef，Controller 自动生成
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-template
```

### 1. 集群环境要求

| 组件 | 版本要求 |
|------|----------|
| Kubernetes | 1.28.x - 1.34.x |
| Container Runtime | containerd 1.6+ |
| OS | Ubuntu 20.04/22.04/24.04, CentOS 7/8, RHEL 8/9 |
| CPU | 2 Core+ |
| Memory | 2GB+ |
| Disk | 50GB+ |

## 前置要求

### 1. 集群环境要求

```bash
# 安装 cert-manager (如果未安装)
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml

# 等待 cert-manager 就绪
kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=300s

# 部署 BYOH Provider (确保 v0.5.20+)
clusterctl init --infrastructure byoh
```

#### 2.2 创建 Bootstrap Token

```bash
# 创建 bootstrap token
TOKEN_ID=$(openssl rand -hex 3)
TOKEN_SECRET=$(openssl rand -hex 8)

kubectl create secret generic -n kube-system bootstrap-token-${TOKEN_ID} \
  --type=bootstrap.kubernetes.io/token \
  --from-literal=token-id=${TOKEN_ID} \
  --from-literal=token-secret=${TOKEN_SECRET} \
  --from-literal=usage-bootstrap-authentication=true \
  --from-literal=usage-bootstrap-signing=true \
  --from-literal=auth-extra-groups=system:bootstrappers:worker
```

#### 2.3 CSR 批准说明

**BYOH Controller 会自动批准 CSR**，无需手动配置。

BYOH Controller 的 `ByoAdmissionReconciler` 会监听 CSR 事件，并自动批准：
- `byoh-csr-*` 格式的 CSR
- `node-csr-*` 格式的 CSR
- 使用 `kubernetes.io/kube-apiserver-client-kubelet` signer 的 CSR

**验证 CSR 批准：**
```bash
# 查看 CSR 状态
kubectl get csr

# 如果 CSR 未被批准，可以手动批准
kubectl certificate approve <csr-name>
```

#### 2.4 Kube-proxy RBAC 权限 (v0.5.83+)

从 v0.5.83 开始，BYOH Agent 需要额外的 RBAC 权限来管理 kube-proxy。需要在集群上部署以下 RBAC 资源：

```bash
# 部署 kube-proxy RBAC
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.83/config/rbac/byohost_kube_proxy_clusterrole.yaml
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.83/config/rbac/byohost_kube_proxy_clusterrolebinding.yaml
```

**说明：**
- `byohost-kube-proxy-role`: 授予 nodes 资源的 get、list、watch 权限
- `byohost-kube-proxy-clusterrole-binding`: 将权限绑定到 `byoh:hosts` 组

**验证 RBAC：**
```bash
# 检查 ClusterRoleBinding
kubectl get clusterrolebinding byohost-kube-proxy-clusterrole-binding -o yaml

# 验证 byoh:hosts 组有节点读取权限
kubectl auth can-it get nodes --as=system:serviceaccount:default:byoh-hostagent
```

### 3. Agent 节点前置要求

在部署 BYOH Agent 之前，需要在每个节点上完成以下准备工作：

#### 3.1 安装 Container Runtime

**Ubuntu/Debian:**
```bash
apt-get update
apt-get install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
# 启用 systemd cgroup driver
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable containerd
systemctl start containerd
```

#### 3.2 下载 Kubernetes 二进制文件

Kubexm 模式需要 kubelet 二进制文件：

```bash
K8S_VERSION=v1.34.1
ARCH=amd64

# 创建目录
mkdir -p /usr/local/bin

# 下载 kubelet (必须)
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubelet" -o /usr/local/bin/kubelet
chmod +x /usr/local/bin/kubelet

# 可选：下载 crictl (用于调试)
CRICTL_VERSION="${K8S_VERSION}"
curl -fsSL "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-${ARCH}.tar.gz" -o /tmp/crictl.tar.gz
tar -xzf /tmp/crictl.tar.gz -C /tmp
mv /tmp/crictl-${CRICTL_VERSION}-linux-${ARCH}/crictl /usr/local/bin/
rm -rf /tmp/crictl.tar.gz /tmp/crictl-${CRICTL_VERSION}-linux-${ARCH}
```

**注意：** kubexm 模式不需要预装 kubeadm，Agent 会自动创建 kubelet.service。

#### 3.3 创建必要目录

```bash
mkdir -p /etc/kubernetes/pki
mkdir -p /var/lib/kubelet
mkdir -p /var/lib/kubelet/pki
mkdir -p /etc/kubernetes/manifests
mkdir -p /etc/systemd/system
```

#### 3.4 下载 BYOH Agent

```bash
AGENT_VERSION=v0.5.20
wget -O /usr/local/bin/byoh-hostagent \
  "https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/${AGENT_VERSION}/byoh-hostagent-linux-amd64"
chmod +x /usr/local/bin/byoh-hostagent
```

## 对接步骤

### 步骤 1: 创建 Cluster 和 ByoCluster

```yaml
# cluster.yaml
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
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: "172.30.1.18"  # 控制平面节点 IP
    port: 6443
```

```bash
kubectl apply -f cluster.yaml
```

### 步骤 2: 创建 Bootstrap Secret

**重要：** Kubexm 模式需要在 ByoMachineTemplate 中引用 Bootstrap Secret。

```yaml
# bootstrap-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-cluster-bootstrap
  namespace: default
type: cluster.x-k8s.io/secret
data:
  # ca.crt (base64 encoded)
  ca.crt: |
    $(cat /etc/kubernetes/pki/ca.crt | base64 -w0)

  # bootstrap-kubeconfig (base64 encoded)
  bootstrap-kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        certificate-authority-data: $(cat /etc/kubernetes/pki/ca.crt | base64 -w0)
        server: https://172.30.1.18:6443
      name: kubernetes
    contexts:
    - context:
        cluster: kubernetes
        user: tls-bootstrap-token-user
      name: default
    current-context: default
    users:
    - name: tls-bootstrap-token-user
      user:
        token: ${TOKEN_ID}.${TOKEN_SECRET}
```

```bash
# 生成并应用
export TOKEN_ID="your-token-id"
export TOKEN_SECRET="your-token-secret"
export CA_CRT=$(cat /etc/kubernetes/pki/ca.crt | base64 -w0)

kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: my-cluster-bootstrap
  namespace: default
type: cluster.x-k8s.io/secret
stringData:
  ca.crt: |
    $CA_CRT
  bootstrap-kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        certificate-authority-data: $CA_CRT
        server: https://172.30.1.18:6443
      name: kubernetes
    contexts:
    - context:
        cluster: kubernetes
        user: tls-bootstrap-token-user
      name: default
    current-context: default
    users:
    - name: tls-bootstrap-token-user
      user:
        token: ${TOKEN_ID}.${TOKEN_SECRET}
EOF
```

### 步骤 3: 创建 ByoMachineTemplate (必须使用 tlsBootstrap 模式)

```yaml
# byomachinetemplate.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec:
      # 必须指定 tlsBootstrap 模式
      joinMode: tlsBootstrap
      # 引用 Bootstrap Secret
      bootstrapSecret:
        name: my-cluster-bootstrap
        namespace: default
```

```bash
kubectl apply -f byomachinetemplate.yaml
```

### 步骤 4: 在节点上启动 Agent

**ByoHost 由 Agent 自动创建**，无需手动创建！

在每个节点上执行：

```bash
# 获取集群 kubeconfig
export KUBECONFIG=/etc/kubernetes/admin.conf
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf

# 启动 Agent（自动注册 ByoHost）
nohup byoh-hostagent \
  --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf \
  > /var/log/byoh-agent.log 2>&1 &
```

### 步骤 5: 创建 MachineDeployment (扩容)

```yaml
# machinedeployment.yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: my-cluster-workers
  namespace: default
spec:
  clusterName: my-cluster
  replicas: 1  # 节点数量
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    metadata:
      labels:
        cluster.x-k8s.io/cluster-name: my-cluster
    spec:
      clusterName: my-cluster
      version: v1.34.1
      bootstrap:
        # Kubexm 模式不需要 KubeadmConfig
        # 直接引用 ByoMachineTemplate
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-template
```

```bash
kubectl apply -f machinedeployment.yaml
```

### 步骤 7: 验证节点加入

```bash
# 观察 Machine 状态
kubectl get machines -w

# 观察 ByoHost 状态
kubectl get byohosts -w

# 检查节点是否加入
kubectl get nodes

# 查看 Agent 日志
ssh node10 'tail -50 /var/log/byoh-agent.log'
```

## 集群纳管完整流程

将现有集群纳入 CAPI 管理，只需创建以下 6 个资源：

```bash
# 1. 部署 BYOH Provider
clusterctl init --infrastructure byoh

# 2. 创建 Cluster + ByoCluster + MachineDeployment + KubeadmConfigTemplate + ByoMachineTemplate + K8sInstallerConfigTemplate
kubectl apply -f scale-up-existing-cluster.yaml
```

**scale-up-existing-cluster.yaml 完整配置：**

```yaml
# Cluster 对象：定义集群网络
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
# ByoCluster 对象：指向现有 API Server
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: "172.30.1.18"  # 控制平面节点 IP
    port: 6443
---
# MachineDeployment 对象：定义 Worker 节点
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
# ByoMachineTemplate 对象：定义安装模板
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
        namespace: default
---
# K8sInstallerConfigTemplate 对象：定义安装包
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

### 3. 在节点上启动 Agent

Agent 启动后会自动创建 ByoHost：

```bash
# 在每个节点上执行
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &
```

### 4. 验证节点加入

```bash
# 查看 Machine 状态
kubectl get machines -n default

# 查看 ByoHost (自动创建)
kubectl get byohosts -n default

# 查看节点
kubectl get nodes
```

**自动发生的事件：**
1. Agent 启动 → 自动注册 ByoHost
2. Controller 分配 ByoHost 给 Machine
3. Agent 收到 MachineRef → 安装 kubelet → 加入集群
4. 缩容时：Machine 删除 → Agent 清理 → Node 对象删除

## Cluster Autoscaler 对接

### 1. 部署 Cluster Autoscaler

```bash
AUTOSCALER_VERSION=v1.34.0
kubectl apply -f \
  "https://raw.githubusercontent.com/kubernetes/autoscaler/releases/download/${AUTOSCALER_VERSION}/cluster-autoscaler.yaml"
```

### 2. 配置 Autoscaler

修改 autoscaler deployment：

```yaml
args:
  - --cloud-provider=clusterapi
  - --clusterapi-cloud-config=/etc/kubernetes/cloud-config
  - --clusterapi-kubeconfig=/etc/kubernetes/kubeconfig
  - --nodes=1:10:default/my-cluster-workers
  - --scale-down-delay-after-add=5m
  - --scale-down-unneeded-time=10m
```

### 3. 为 Autoscaler 创建 Kubeconfig

```bash
# 创建 service account
kubectl apply -f - << 'EOF'
apiVersion: v1
kind: ServiceAccount
metadata:
  name: cluster-autoscaler
  namespace: kube-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-autoscaler
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: cluster-autoscaler
  namespace: kube-system
EOF

# 生成 kubeconfig
SECRET_NAME=$(kubectl get sa cluster-autoscaler -n kube-system -o jsonpath='{.secrets[0].name}')
CA_CRT=$(kubectl get secret $SECRET_NAME -n kube-system -o jsonpath='{.data.ca\.crt}' | base64 -d)
TOKEN=$(kubectl get secret $SECRET_NAME -n kube-system -o jsonpath='{.data.token}' | base64 -d)

cat > /etc/kubernetes/kubeconfig <<EOF
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority: /etc/kubernetes/ca.crt
    server: https://172.30.1.18:6443
  name: default
contexts:
- context:
    cluster: default
    user: cluster-autoscaler
  name: default
current-context: default
users:
- name: cluster-autoscaler
  user:
    token: $TOKEN
EOF
```

### 4. Autoscaler 与 ByoMachineTemplate

v0.5.20+ 版本已添加 Status.Capacity 支持，但需要在 K8sInstallerConfig 中提供节点配置：

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: K8sInstallerConfigTemplate
metadata:
  name: my-cluster-worker-installer
  namespace: default
spec:
  template:
    spec:
      bundleRepo: ""
      bundleType: ""
      # 可以在这里配置节点资源信息
```

## 故障排查

### 问题 1: Agent 报错 "failed to write bootstrap kubeconfig"

**错误信息：**
```
failed to write bootstrap kubeconfig: open /etc/kubernetes/bootstrap-kubeconfig: no such file or directory
```

**解决：** 此问题已在 v0.5.20 修复。Agent 现在会自动创建 `/etc/kubernetes/` 目录。

**如果仍在旧版本：**
```bash
# 手动创建目录
mkdir -p /etc/kubernetes/pki
mkdir -p /var/lib/kubelet
```

### 问题 2: kubelet 启动失败

```bash
# 检查 kubelet 状态
systemctl status kubelet

# 检查日志
journalctl -u kubelet --no-pager -n 100

# 常见原因：
# 1. 缺少 CA 证书
ls -la /etc/kubernetes/pki/

# 2. bootstrap-kubeconfig 格式错误
cat /etc/kubernetes/bootstrap-kubeconfig

# 3. CSR 未被批准
kubectl get csr
kubectl get csr -o jsonpath='{.items[?(@.spec.username=="system:bootstrap:worker")].status.conditions}'
```

### 问题 3: TLS Bootstrap CSR 未被批准

```bash
# 检查 CSR 状态
kubectl get csr

# 查看 CSR 详情
kubectl get csr <csr-name> -o yaml

# 手动批准
kubectl certificate approve <csr-name>

# 检查 RBAC
kubectl auth can-it create certificatesigningrequests
```

### 问题 4: 节点加入后 NotReady

```bash
# 检查节点条件
kubectl describe node <node-name>

# 常见原因：
# 1. CNI 未安装
# 2. 网络插件未配置
# 3. 节点还未完全就绪

# 等待节点就绪
kubectl wait --for=condition=Ready node/<node-name> --timeout=300s
```

### 问题 5: Agent 无法连接到集群

```bash
# 检查 kubeconfig
cat /root/byoh/bootstrap-kubeconfig.conf

# 测试连接
curl -k --header "Authorization: Bearer $(cat /root/byoh/bootstrap-kubeconfig.conf | grep token | cut -d' ' -f2)" \
  https://172.30.1.18:6443/healthz

# 检查网络
ping 172.30.1.18
telnet 172.30.1.18 6443
```

### 问题 6: kubelet CSR 被拒绝

**错误信息：**
```
Cannot create certificate signing request: User "system:bootstrap:worker" cannot create resource "certificatesigningrequests" in API group "certificates.k8s.io"
```

**解决：** 检查 RBAC 配置
```bash
# 验证 ClusterRoleBinding
kubectl get clusterrolebinding byoh:kubelet-bootstrap -o yaml

# 检查 subject 是否正确
kubectl get group system:bootstrappers:worker
```

### 问题 7: 缩容后 Node 对象残留 (v0.5.80+)

**问题描述：** 当 MachineDeployment 缩容时，Node 对象未从集群中删除，导致节点资源残留。

**原因分析：** 在 v0.5.80 之前，`hostCleanUp()` 函数仅在 `K8sComponentsInstallationSucceeded` 条件为 True 时才调用 `resetNodeWithRetry()`。缩容时该条件可能为 False，导致 Node 对象未被删除。

**解决：** 此问题已在 v0.5.80 修复。Agent 现在无论 `K8sComponentsInstallationSucceeded` 状态如何，都会在清理时删除 Node 对象。

**验证缩容是否正常工作：**
```bash
# 1. 缩容 MachineDeployment
kubectl scale machinedeployment my-cluster-workers -n default --replicas=0

# 2. 验证 Node 对象被删除
kubectl get nodes | grep node10

# 3. 查看 Agent 日志确认 Node 删除
ssh node10 'journalctl -u byoh-hostagent.service --since "1 minute ago" --no-pager | grep -E "Deleting Node|Successfully deleted"'

# 预期输出：
# I0105 10:30:45.123456 12345 host_reconciler.go:615] Deleting Node object from API server, node=node10
# I0105 10:30:45.234567 12345 host_reconciler.go:621] Successfully deleted Node object, node=node10
```

**如果仍在旧版本遇到此问题：**
```bash
# 手动删除残留的 Node 对象
kubectl delete node <node-name>
```

## 快速参考

### 完整命令清单

```bash
# 1. 集群上：部署 Provider
clusterctl init --infrastructure byoh

# 2. 集群上：创建 RBAC
kubectl apply -f https://raw.githubusercontent.com/mensylisir/cluster-api-provider-bringyourownhost/v0.5.20/config/rbac/byoh-rbac.yaml

# 3. 集群上：创建 Bootstrap Secret
TOKEN_ID=$(openssl rand -hex 3)
TOKEN_SECRET=$(openssl rand -hex 8)
CA_CRT=$(cat /etc/kubernetes/pki/ca.crt | base64 -w0)
kubectl create secret generic -n kube-system bootstrap-token-${TOKEN_ID} --type=bootstrap.kubernetes.io/token --from-literal=token-id=${TOKEN_ID} --from-literal=token-secret=${TOKEN_SECRET} --from-literal=usage-bootstrap-authentication=true --from-literal=usage-bootstrap-signing=true --from-literal=auth-extra-groups=system:bootstrappers:worker

# 4. 集群上：创建 Bootstrap Secret (用于 Agent)
kubectl apply -f bootstrap-secret.yaml

# 5. 集群上：创建 ByoMachineTemplate
kubectl apply -f byomachinetemplate.yaml

# 6. 集群上：创建 ByoHost
kubectl apply -f byohost.yaml

# 7. 节点上：安装 containerd
apt-get install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd

# 8. 节点上：下载 kubelet
K8S_VERSION=v1.34.1
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/amd64/kubelet" -o /usr/local/bin/kubelet
chmod +x /usr/local/bin/kubelet

# 9. 节点上：下载并启动 Agent
wget -O /usr/local/bin/byoh-hostagent "https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/v0.5.20/byoh-hostagent-linux-amd64"
chmod +x /usr/local/bin/byoh-hostagent
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf > /var/log/byoh-agent.log 2>&1 &

# 10. 集群上：创建 MachineDeployment
kubectl apply -f machinedeployment.yaml
```
