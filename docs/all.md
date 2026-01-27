# BYOH 对接与部署完整指南 (Complete Guide)

这份文档旨在为您提供从零开始构建 BYOH (Bring Your Own Host) 私有云集群的完整操作手册。涵盖了环境准备、主机接入、以及两种核心部署模式（Kubeadm 与 Kubexm）的详细说明。

## 0. 验证环境与工具（常见问题）

**Q: 为什么运行 `clusterctl version` 会卡住？**
A: `clusterctl` 在输出本地版本后，会默认尝试连接 GitHub 检查是否有新版本，还会连接集群获取服务端版本。
- 如果您看到类似 `clusterctl version: &version.Info{...}` 的输出后卡住，说明本地版本已成功打印。
- 此时卡住是因为无法连接 GitHub（离线环境），您可以直接按 **Ctrl+C** 结束命令，不影响使用。

## 1. 准备离线资源架构角色

- **控制节点 (Management Cluster)**: 安装了 Cluster API 和 BYOH Controller 的 Kubernetes 集群（大脑）。
- **计算节点 (Agent Host)**: 您的闲置 Linux 物理机或虚拟机，运行 `byoh-hostagent`（苦力）。
- **业务集群 (Workload Cluster)**: 最终由 BYOH 创建出来的、运行用户应用的 Kubernetes 集群。

---

## 2. 第一阶段：初始化控制节点

假设您已经有了一个 Kubernetes 集群（Kind/Minikube/现有集群）作为控制节点，并且已经准备好了所有 BYOH 组件文件。

### 2.1 安装与配置核心组件 (离线环境准备)

在执行初始化之前，我们需要先准备好环境。假设您已经将所有必要文件下载到了 `~/wode` 目录。

**第一步：安装 clusterctl 工具**
```bash
# 进入存放文件的目录
cd ~/wode/cluster-api/

# 赋予执行权限
chmod +x clusterctl-linux-amd64

# 移动到系统路径 (重命名为 clusterctl)
cp clusterctl-linux-amd64 /usr/local/bin/clusterctl

# 验证安装
clusterctl version
```

**第二步：配置本地仓库 (Overrides)**
为了让 `clusterctl` 在离线环境下工作，我们需要将下载的 YAML 文件放置到特定的目录结构中，欺骗 `clusterctl` 让它以为这些是从网上下载的。

*(注意：请根据实际下载的版本调整路径中的 v1.4.4 和 v0.1.0)*

```bash
# === 1. 配置 Cluster API 核心组件 (Core) ===
mkdir -p ~/.cluster-api/overrides/cluster-api/v1.4.4/
cp ~/wode/cluster-api/core-components.yaml ~/.cluster-api/overrides/cluster-api/v1.4.4/

# === 2. 配置 Bootstrap 组件 (Kubeadm) ===
mkdir -p ~/.cluster-api/overrides/bootstrap-kubeadm/v1.4.4/
cp ~/wode/cluster-api/bootstrap-components.yaml ~/.cluster-api/overrides/bootstrap-kubeadm/v1.4.4/

# === 3. 配置 Control Plane 组件 (Kubeadm) ===
mkdir -p ~/.cluster-api/overrides/control-plane-kubeadm/v1.4.4/
cp ~/wode/cluster-api/control-plane-components.yaml ~/.cluster-api/overrides/control-plane-kubeadm/v1.4.4/

# === 4. 配置 BYOH Infrastructure 插件 ===
mkdir -p ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
cp ~/wode/byoh/infrastructure-components.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
cp ~/wode/byoh/metadata.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
```

**第三步：执行初始化**
现在环境已经准备就绪，我们可以命令 `clusterctl` 使用这些本地文件来初始化管理集群。

```bash
clusterctl init \
  --core cluster-api:v1.4.4 \
  --bootstrap kubeadm:v1.4.4 \
  --control-plane kubeadm:v1.4.4 \
  --infrastructure byoh:v0.1.0
```

*验证：执行 `kubectl get pods -A | grep byoh` 确认 Controller 正在运行。*

---

## 3. 第二阶段：物理机接入 (Host Preparation)

这是最关键的一步，我们需要让闲置机器向控制节点注册。

### 3.1 获取注册凭证 (Kubeconfig 怎么来？)

Agent 需要一个 Kubeconfig 才能和控制节点通信。为了安全，我们通常创建一个权限受限的 `BootstrapKubeconfig`，但在测试环境中，您可以直接使用管理员 Kubeconfig，或者生成一个**专用注册配置**。

**方法 A：生成专用注册配置（推荐，更安全）**
在**控制节点**上执行：

```bash
# 1. 获取 API Server 地址
APISERVER=$(kubectl config view -ojsonpath='{.clusters[0].cluster.server}')
# 2. 获取 CA 证书数据
CA_CERT=$(kubectl config view --flatten -ojsonpath='{.clusters[0].cluster.certificate-authority-data}')

# 3. 创建 BootstrapKubeconfig 资源（告诉 Controller 允许注册）
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

# 4. 导出为文件 (这就是 Agent 需要的钥匙)
kubectl get bootstrapkubeconfig bootstrap-kubeconfig -n default -o=jsonpath='{.status.bootstrapKubeconfigData}' > agent-bootstrap.conf
```

**方法 B：直接使用管理员配置（仅测试用）**
```bash
kubectl config view --flatten --minify > agent-bootstrap.conf
```

### 3.2 传输文件到物理机

假设您的闲置机器 IP 为 `192.168.1.100`，用户为 `root`。在控制节点（或您的操作机）上执行：

```bash
# 1. 传输 Agent 二进制程序
scp ~/wode/byoh/byoh-hostagent-linux-amd64 root@192.168.1.100:/root/

# 2. 传输刚才生成的配置文件
scp agent-bootstrap.conf root@192.168.1.100:/root/
```

### 3.3 启动 Agent (在物理机上执行)

登录到闲置机器 `192.168.1.100`：

```bash
# 1. 赋予执行权限
chmod +x byoh-hostagent-linux-amd64

# 2. 启动 Agent
# --bootstrap-kubeconfig 指定刚才传过来的配置文件
# --work-dir 指定工作目录（可选）
sudo ./byoh-hostagent-linux-amd64 --bootstrap-kubeconfig ./agent-bootstrap.conf &

# (可选) 查看日志确认
tail -f byoh-hostagent.log
```

### 3.4 验证注册

回到**控制节点**，查看主机池：

```bash
kubectl get byohosts
```
如果看到状态为 `Available` 的主机，说明接入成功！

---

## 4. 第三阶段：创建业务集群 (部署模式)

BYOH 支持两种节点引导模式。您可以在创建集群时的 `ByoMachineTemplate` 中进行选择。

### 模式一：Kubeadm 模式 (标准模式)
这是默认模式。Agent 会自动下载并调用 `kubeadm join` 将节点加入集群。

**适用场景**：
- 标准 Kubernetes 部署。
- 依赖 `kubeadm` 工具链。

**配置方法**：
在 `ByoMachineTemplate` 中，`spec.template.spec.joinMode` 设置为 `kubeadm`（或留空，默认为 kubeadm）。

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker
spec:
  template:
    spec:
      joinMode: kubeadm  # <--- 关键点
      # ... 其他配置
```

### 模式二：Kubexm (TLS Bootstrap) 模式
这是一种轻量级模式。Agent 不使用 `kubeadm join`，而是直接安装 Kubernetes 二进制文件 (kubelet, kube-proxy)，并通过 TLS Bootstrap 协议直接向 API Server 申请证书加入集群。

**适用场景**：
- 无法运行 kubeadm 的环境。
- 需要更精细控制二进制安装过程。
- 追求更快的节点启动速度。

**配置方法**：
需要显式设置 `joinMode` 为 `tlsBootstrap`。

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ByoMachineTemplate
metadata:
  name: my-cluster-worker-kubexm
spec:
  template:
    spec:
      joinMode: tlsBootstrap  # <--- 启用 Kubexm 模式
      
      # 可选配置:
      downloadMode: online    # online (在线下载) 或 offline (使用本地二进制)
      kubernetesVersion: v1.26.0
      manageKubeProxy: true   # 是否由 Agent 管理 kube-proxy 进程

      # 注意：Kubexm 模式支持自动同步 kubelet-config.yaml 和 kube-proxy 配置。
      # 如果您的 Bootstrap Secret 中包含这些配置，Agent 会自动应用它们。
```

> **⚠️ Kubexm 离线模式特别说明**：
> 1. 如果设置 `downloadMode: offline`，您必须提前手动将 k8s 二进制文件 (`kubelet`, `kube-proxy`, `kubectl`) 放置在物理机的 `/usr/local/bin/` 目录下，否则 Agent 启动会失败。
> 2. **清理逻辑安全增强**：Agent 现在会检测环境。如果未发现 `kubeadm`，在重置节点时将执行“软清理”（停止服务并删除配置），而不会尝试运行 `kubeadm reset`，这完美适配纯二进制部署环境。

---

## 5. 第四阶段：部署自动扩缩容 (Autoscaler)

当业务集群创建成功（无论是 Kubeadm 还是 Kubexm 模式），您都可以部署 Cluster Autoscaler 来实现自动化管理。

### 5.1 部署 Autoscaler
使用 Helm 或 YAML 部署，关键参数如下：

- `cloudProvider`: `clusterapi`
- `autoDiscovery.clusterName`: `<您的业务集群名>`
- `image.tag`: 与您的 K8s 版本匹配（例如 `v1.26.0`）

### 5.2 启用扩缩容
在 `MachineDeployment` 对象中添加注解：

```yaml
metadata:
  annotations:
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "10"
```

---

## 常见问题排查

1. **Agent 启动报错 "connection refused"**
   - 检查 `agent-bootstrap.conf` 中的 API Server 地址物理机是否能 ping 通。
   - 检查控制节点的防火墙是否放行了 API Server 端口（通常是 6443）。

2. **Kubexm 模式下节点一直无法 Ready**
   - 检查物理机上的 `kubelet` 服务状态：`systemctl status kubelet`。
   - 检查是否缺少 CNI 插件（Kubexm 模式通常需要手动或通过 DaemonSet 安装 CNI）。

3. **Autoscaler 不扩容**
   - 检查 Pod 是否处于 Pending 状态。
   - 检查 `ByoHost` 池子里是否还有处于 `Available` 状态的空闲机器。
