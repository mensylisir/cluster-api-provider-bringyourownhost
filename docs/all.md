这份手册旨在让你从零开始，把一堆闲置的 Linux 服务器（裸机或虚拟机）变成一个**自动扩缩容**的私有云集群。

我们把整个系统分为三个角色：

1. **💻 开发机**：你的电脑，用来编译程序、打包镜像。
2. **🧠 管理集群 (Management Cluster)**：控制中心（大脑），负责下令。
3. **🖥️ 闲置主机池 (Host Pool)**：你的那堆裸机，负责干活。

------

### 第一步：在【开发机】上打包全家桶

**目标**：把代码变成镜像和安装包。

1. **准备环境**：确保你已经按照之前的步骤配置好了 Docker 代理和 binfmt（用于支持双架构编译）。

2. **一键打包**：
   在项目根目录下执行：

   ```bash
   # 把镜像推送到你的仓库，并生成所有部署文件
   # 注意：版本号 v0.1.0 要与 Makefile 默认保持一致，或者是你想要发布的版本
   make build-release-artifacts IMG=docker.io/mensyli/cluster-api-byoh-controller:v0.1.0
   ```

3. **检查成果**：
   执行完后，你会看到一个 `_dist` 文件夹，里面必须有这几样东西：

   - `infrastructure-components.yaml`：**大脑的插件**（Controller 部署文件）。
   - `byoh-hostagent-linux-amd64`：**主机的代理程序**（Agent 二进制）。
   - `metadata.yaml`：**版本说明书**。
   - `cluster-template.yaml`：**集群创建模板**。

------

### 第二步：在【管理集群】安装大脑

**目标**：让控制中心学会怎么管理这些裸机。

1. **先装“护送程序” (cert-manager)**：
   BYOH 必须依赖它来管理安全证书，不装的话控制器起不来。

   ```bash
   kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml
   ```

2. **让大脑认识你的 BYOH 插件**：
   我们需要告诉 clusterctl（管理集群的工具）去哪里找你刚才生成的插件。

   ```bash
   # 创建配置目录（注意版本号要和 metadata.yaml 里的一致）
   mkdir -p ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   
   # 把刚才生成的成品拷进去
   cp _dist/infrastructure-components.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   cp _dist/metadata.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   ```

3. **初始化大脑**：

   ```bash
   clusterctl init --infrastructure byoh:v0.1.0
   ```

   *验证：执行 `kubectl get pods -A`，看到 `cabyoh-system` 命名空间下的 pod 运行正常，大脑就装好了。*

------

### 第三步：在【闲置主机】上报到

**目标**：让你那几台裸机向大脑注册，加入“资源池”。

1. **准备主机**：找一台装好 Ubuntu 的干净机器。

2. **获取注册凭证 (Bootstrap Config)**：
   在**管理集群**上运行以下命令，生成一个临时的注册配置文件：

   ```bash
   # 获取 API Server 地址和 CA 证书
   APISERVER=$(kubectl config view -ojsonpath='{.clusters[0].cluster.server}')
   CA_CERT=$(kubectl config view --flatten -ojsonpath='{.clusters[0].cluster.certificate-authority-data}')

   # 创建 BootstrapKubeconfig CR (告诉大脑允许注册)
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

   # 导出配置文件
   kubectl get bootstrapkubeconfig bootstrap-kubeconfig -n default -o=jsonpath='{.status.bootstrapKubeconfigData}' > bootstrap-kubeconfig.conf
   ```

3. **运行 Agent**：
   把 `_dist/byoh-hostagent-linux-amd64` 和刚才生成的 `bootstrap-kubeconfig.conf` 传到你的闲置主机上。

   ```bash
   chmod +x byoh-hostagent-linux-amd64
   
   # 首次运行（进行注册）
   sudo ./byoh-hostagent-linux-amd64 --bootstrap-kubeconfig bootstrap-kubeconfig.conf
   ```

   *Agent 会自动检测你的 CPU、内存以及 **NVIDIA GPU**，并将这些信息上报给管理集群。*

4. **在“大脑”上查收**：
   回到你的管理集群，运行：

   ```bash
   kubectl get byohosts
   ```

   如果你看到主机的名字，状态是 **Available**，且 `AGE` 在增加，说明注册成功！

   *进阶：如果想让 Agent 开机自启，请参考 `hack/install-host-agent-service.sh` 脚本配置 Systemd 服务（需在首次注册成功生成 `~/.byoh/config` 后执行）。*

------

### 第四步：部署【自动扩缩容】组件 (CAS)

**目标**：让系统学会“没地方跑 Pod 时，自动去池子里抓机器”。

1. **准备配置文件**：
   你需要创建两个文件：`cluster-autoscaler-rbac.yaml` 和 `cluster-autoscaler-deployment.yaml`。
   
   *请参考 `docs/autoscaler.md` 中的详细配置内容，那里有完整的 YAML 示例。*

2. **核心参数确认**：
   在 `deployment.yaml` 中，确保启动参数包含：

   - `--cloud-provider=clusterapi`
   - `--namespace=default` (你的 workload cluster 所在的 namespace)
   - `--node-group-auto-discovery=clusterapi:clusterName=my-cool-cluster` (可选，或通过 annotation 自动发现)

3. **部署**：

   ```bash
   kubectl apply -f cluster-autoscaler-rbac.yaml
   kubectl apply -f cluster-autoscaler-deployment.yaml
   ```

------

### 第五步：创建你的第一个自动化集群

**目标**：真正让 CA (Cluster API) 接管你的 BYOH。

1. **生成集群 YAML**：

   ```bash
   clusterctl generate cluster my-cool-cluster --flavor vm --kubernetes-version v1.25.5 > my-cluster.yaml
   ```

2. **开启自动伸缩开关**（重要！）：
   打开 `my-cluster.yaml`，找到 `MachineDeployment` 这一节，在 `metadata.annotations` 下面加上这两行：

   ```yaml
   metadata:
     annotations:
       cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
       cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "5" # 你池子里有多少机器就写多少
   ```

3. **（可选）指定 GPU 机器**：
   如果你想让这个集群只使用 GPU 机器，请修改 `ByoMachineTemplate` 的 selector：

   ```yaml
   spec:
     template:
       spec:
         selector:
           matchLabels:
             nvidia.com/gpu.count: "1" # 只要有 GPU 的机器
   ```

4. **应用配置**：

   ```bash
   kubectl apply -f my-cluster.yaml
   ```

------

### 傻瓜都能懂的原理（它是怎么“对接”的？）

1. **Pod 没地方跑了**：业务集群里突然来了很多流量，或者你提交了一个需要 GPU 的 AI 任务。
2. **CAS (自动扩缩容组件) 发现了**：它看到 Pod 在排队，Pending 了。
3. **Cluster API (核心) 响应了**：它发现 MachineDeployment 允许扩容，于是创建一个新的 `Machine` 对象。
4. **BYOH Controller (大脑插件) 动手了**：它看到新 `Machine` 诞生，且如果有 GPU 需求，它会去 `ByoHost` 池子里筛选带有 `nvidia.com/gpu.count` 标签的空闲机器。
5. **绑定与安装**：找到机器后，Controller 把它们**绑定**。Agent 收到指令，自动执行 `kubeadm join`。
6. **成功扩容**：几分钟后，新节点 Ready，你的 AI 任务开始运行。

**总结一句话：你只管往池子里加机器（跑 Agent），剩下的扩容缩容，全自动。**
