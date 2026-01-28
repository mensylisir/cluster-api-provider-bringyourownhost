# Kubeadm 模式对接指南

## ✅ BYOH 自动处理事项

| 组件 | 自动处理 | 说明 |
|------|----------|------|
| **CSR 批准** | ✅ 自动 | ByoAdmissionReconciler 自动批准 CSR |
| **RBAC 权限** | ✅ 自动 | 部署 BYOH Provider 时自动创建 |
| **节点安装** | ✅ 自动 | Agent 执行 K8sInstallerConfig 中的安装脚本 |

## 需要手动处理 ⚠️

| 步骤 | 操作 | 说明 |
|------|------|------|
| 1 | 创建 Bootstrap Token | kubeadm join 需要有效的 token |
| 2 | 创建 KubeadmConfig | 提供 kubeadm join 配置 |

---

## 自动创建 Token 配置

### 指定 bootstrapConfigRef (推荐)

在 MachineDeployment 中使用 `KubeadmConfig` 引用，Controller 会自动创建 Bootstrap Token：

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: my-cluster-workers
  namespace: default
spec:
  template:
    spec:
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfig
          name: my-cluster-workers-config
---
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
```

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
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfig
          name: my-cluster-workers-config
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
    spec: {}  # kubeadm 模式不需要额外配置
---
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
```

### 步骤 4: 创建 ByoHost

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHost
metadata:
  name: node10
  namespace: default
spec: {}  # kubeadm 模式不需要额外配置
```

### 步骤 5: 启动 Agent

```bash
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf
nohup byoh-hostagent \
  --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf \
  > /var/log/byoh-agent.log 2>&1 &
```

```bash
# 安装 cert-manager (如果未安装)
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.2/cert-manager.yaml

# 等待 cert-manager 就绪
kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=300s

# 部署 BYOH Provider
clusterctl init --infrastructure byoh
```

#### 2.2 创建 Bootstrap Token (用于 CSR 签名)

```bash
# 创建 bootstrap token (如果集群中没有)
kubectl create secret generic -n kube-system bootstrap-token-abcdef \
  --type=bootstrap.kubernetes.io/token \
  --from-literal=token-id=abcdef \
  --from-literal=token-secret=1234567890abcdef \
  --from-literal=usage-bootstrap-authentication=true \
  --from-literal=usage-bootstrap-signing=true \
  --from-literal=auth-extra-groups=system:bootstrappers:kubeadm:default-node-token
```

#### 2.3 CSR 批准说明

**BYOH Controller 会自动批准 CSR**，无需手动配置 RBAC。

部署 BYOH Provider 时，以下 RBAC 会自动创建：
- CSR 读取和批准权限
- Signer (kubernetes.io/kube-apiserver-client, kubernetes.io/kubelet-serving) 的 approve 权限

**验证 CSR 批准：**
```bash
# 查看 CSR 状态
kubectl get csr

# 如果 CSR 未被批准，可以手动批准
kubectl certificate approve <csr-name>
```

### 3. Agent 节点前置要求

在部署 BYOH Agent 之前，需要在每个节点上完成以下准备工作：

#### 3.1 安装 Container Runtime

**Ubuntu/Debian:**
```bash
# 安装 containerd
apt-get update
apt-get install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable containerd
systemctl start containerd
```

**CentOS/RHEL:**
```bash
# 安装 containerd
yum install -y containerd
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable containerd
systemctl start containerd
```

#### 3.2 安装 Kubernetes 二进制文件

```bash
# 下载并安装 kubelet, kubeadm, kubectl
K8S_VERSION=v1.34.1
ARCH=amd64

# 下载kubelet
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubelet" -o /usr/local/bin/kubelet
chmod +x /usr/local/bin/kubelet

# 下载kubeadm
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubeadm" -o /usr/local/bin/kubeadm
chmod +x /usr/local/bin/kubeadm

# 下载kubectl
curl -fsSL "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubectl" -o /usr/local/bin/kubectl
chmod +x /usr/local/bin/kubectl
```

#### 3.3 创建必要的目录

```bash
mkdir -p /etc/kubernetes/pki
mkdir -p /var/lib/kubelet
mkdir -p /etc/kubernetes/manifests
```

#### 3.4 下载 BYOH Agent

```bash
# 从 GitHub Releases 下载
AGENT_VERSION=v0.5.20
wget -O /usr/local/bin/byoh-hostagent \
  "https://github.com/mensylisir/cluster-api-provider-bringyourownhost/releases/download/${AGENT_VERSION}/byoh-hostagent-linux-amd64"
chmod +x /usr/local/bin/byoh-hostagent
```

## 对接步骤

### 步骤 1: 创建 ByoCluster 资源

```yaml
# byocluster.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  controlPlaneEndpoint:
    host: "172.30.1.18"  # 替换为你的控制平面节点 IP
    port: 6443
```

```bash
kubectl apply -f byocluster.yaml
```

### 步骤 2: 创建 ByoHost 资源 (注册节点)

```yaml
# byohost.yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHost
metadata:
  name: node10
  namespace: default
  labels:
    site: default
    node-role.kubernetes.io/worker: ""
spec:
  # 不需要指定 bootstrapSecret，kubeadm 模式会自动处理
  uninstallationScript: |
    set -euox pipefail
    kubeadm reset -f || true
    systemctl stop kubelet || true
    systemctl disable kubelet || true
    rm -rf /etc/kubernetes/pki
    rm -rf /var/lib/kubelet
    rm -rf /etc/kubernetes/manifests
```

```bash
kubectl apply -f byohost.yaml
```

### 步骤 3: 启动 Agent

在每个节点上执行：

```bash
# 生成 kubeconfig (用于 Agent 访问集群)
export KUBECONFIG=/etc/kubernetes/admin.conf
clusterctl get kubeconfig my-cluster > /root/byoh/bootstrap-kubeconfig.conf

# 启动 Agent
nohup byoh-hostagent \
  --kubeconfig=/root/byoh/bootstrap-kubeconfig.conf \
  > /var/log/byoh-agent.log 2>&1 &
```

### 步骤 4: 创建 MachineDeployment (扩容节点)

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
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfig
          name: my-cluster-workers-config
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ByoMachineTemplate
        name: my-cluster-worker-template
---
# ByoMachineTemplate
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec: {}  # kubeadm 模式不需要额外配置
---
# KubeadmConfig (生成 kubeadm join 命令)
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
        # 如果需要指定 providerID，可以取消注释
        # provider-id: byoh://${HOSTNAME}/${NAMESPACE}/${NAME}
```

```bash
kubectl apply -f machinedeployment.yaml
```

## 集群纳管完整流程

### 将现有集群纳入 CAPI 管理

如果希望将已存在的 Kubernetes 集群纳入 Cluster API 管理：

```bash
# 1. 确保集群上已部署 CAPI 和 BYOH Provider (见步骤 2)

# 2. 创建 Cluster 资源
kubectl apply -f - << 'EOF'
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
EOF

# 3. 创建 ByoHost 并注册所有现有节点
for node in ai18 ai20 node10; do
  kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoHost
metadata:
  name: $node
  namespace: default
spec:
  uninstallationScript: |
    kubeadm reset -f 2>/dev/null || true
EOF
done

# 4. 在每个节点上启动 Agent (见步骤 3)
```

## Cluster Autoscaler 对接

### 1. 部署 Cluster Autoscaler

```bash
# 根据你的 Kubernetes 版本选择对应的 autoscaler 版本
AUTOSCALER_VERSION=v1.34.0

kubectl apply -f \
  "https://raw.githubusercontent.com/kubernetes/autoscaler/releases/download/${AUTOSCALER_VERSION}/cluster-autoscaler.yaml"
```

### 2. 配置 Autoscaler 使用 BYOH

修改 autoscaler deployment 的启动参数：

```yaml
args:
  - --cloud-provider=clusterapi
  - --clusterapi-cloud-config=/etc/kubernetes/cloud-config
  - --clusterapi-kubeconfig=/etc/kubernetes/kubeconfig
  - --nodes=1:10:default/my-cluster-workers  # MachineDeployment 范围
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
kubectl get secret $SECRET_NAME -n kube-system -o jsonpath='{.data.ca\.crt}' | base64 -d > /etc/kubernetes/ca.crt
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

### 4. Autoscaler 对 ByoMachineTemplate 的要求

为了支持 scale-from-zero，ByoMachineTemplate 需要声明节点 capacity：

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-template
  namespace: default
spec:
  template:
    spec: {}
---
# ClusterAutoscaler 使用的 annotations
# 在 MachineDeployment 上添加
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: my-cluster-workers
  namespace: default
  annotations:
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "10"
spec:
  replicas: 1
  # ... 其他配置
  template:
    spec:
      metadata:
        labels:
          cluster.x-k8s.io/cluster-api-autoscaler-node-group-name: my-cluster-workers
```

## 故障排查

### 问题 1: Agent 启动失败

```bash
# 检查日志
journalctl -u byoh-hostagent --no-pager

# 常见原因：
# 1. kubeconfig 权限问题
chmod 600 /root/byoh/bootstrap-kubeconfig.conf

# 2. 网络不通
curl -k https://172.30.1.18:6443/healthz
```

### 问题 2: kubelet 无法启动

```bash
# 检查 kubelet 状态
systemctl status kubelet

# 检查日志
journalctl -u kubelet --no-pager -n 100

# 常见原因：
# 1. cgroup driver 不匹配
#    确保 containerd 的 cgroup driver 与 kubelet 一致
grep -r "SystemdCgroup" /etc/containerd/config.toml

# 2. 缺少 CA 证书
ls -la /etc/kubernetes/pki/
```

### 问题 3: CSR 未被批准

```bash
# 检查 CSR 状态
kubectl get csr

# 手动批准 CSR
kubectl certificate approve <csr-name>
```

### 问题 4: 节点 NotReady

```bash
# 检查节点状态
kubectl describe node <node-name>

# 常见原因：
# 1. CNI 未安装
# 2. kubelet 配置错误
# 3. 证书过期
```
