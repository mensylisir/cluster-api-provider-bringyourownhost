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

   codeBash

   ```
   # 把镜像推送到你的仓库，并生成所有部署文件
   make build-release-artifacts IMG=docker.io/mensyli/cluster-api-byoh-controller:v0.0.1
   ```

3. **检查成果**：
   执行完后，你会看到一个 _dist 文件夹，里面必须有这几样东西：

   - infrastructure-components.yaml：**大脑的插件**。
   - byoh-hostagent-linux-amd64：**主机的代理程序**。
   - metadata.yaml：**版本说明书**。
   - cluster-template.yaml：**集群创建模板**。

------



### 第二步：在【管理集群】安装大脑

**目标**：让控制中心学会怎么管理这些裸机。

1. **先装“护送程序” (cert-manager)**：
   BYOH 必须依赖它来管理安全证书，不装的话控制器起不来。

   codeBash

   ```
   kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml
   ```

2. **让大脑认识你的 BYOH 插件**：
   我们需要告诉 clusterctl（管理集群的工具）去哪里找你刚才生成的插件。

   codeBash

   ```
   # 创建配置目录（注意版本号要和 Makefile 里的 VERSION 对上）
   mkdir -p ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   
   # 把刚才生成的成品考进去
   cp _dist/infrastructure-components.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   cp metadata.yaml ~/.cluster-api/overrides/infrastructure-byoh/v0.1.0/
   ```

3. **初始化大脑**：

   codeBash

   ```
   clusterctl init --infrastructure byoh:v0.1.0
   ```

   *验证：执行 kubectl get pods -A，看到 cabyoh-system 命名空间下的 pod 运行正常，大脑就装好了。*

------



### 第三步：在【闲置主机】上报到

**目标**：让你那几台裸机向大脑注册，加入“资源池”。

1. **准备主机**：找一台装好 Ubuntu 的干净机器。

2. **拷贝文件**：把开发机 _dist 里的 byoh-hostagent-linux-amd64 拷到这台主机的 /usr/local/bin/。

3. **授权并报到**：
   你需要把**管理集群**的 admin.conf（kubeconfig）拷贝到主机的 /etc/kubernetes/agent.conf。
   然后运行项目里的安装脚本，让它变成开机自启的服务：

   codeBash

   ```
   sudo ./hack/install-host-agent-service.sh --kubeconfig /etc/kubernetes/agent.conf
   ```

4. **在“大脑”上查收**：
   回到你的管理集群，运行：

   codeBash

   ```
   kubectl get byohosts
   ```

   如果你看到主机的名字，且状态是 **Available**，恭喜，这台机器已经随时待命了！

------



### 第四步：部署【自动扩缩容】组件 (CAS)

**目标**：让系统学会“没地方跑 Pod 时，自动去池子里抓机器”。

1. **下载官方 CAS 配置文件**：
   从 [Kubernetes 官方 GitHub](https://www.google.com/url?sa=E&q=https%3A%2F%2Fgithub.com%2Fkubernetes%2Fautoscaler%2Ftree%2Fmaster%2Fcluster-autoscaler%2Fcloudprovider%2Fclusterapi%2Fexamples) 下载 cluster-autoscaler-deployment.yaml 和 rbac 文件。

2. **改参数**：
   编辑 deployment.yaml，确保启动参数包含：

   - --cloud-provider=clusterapi
   - --nodes-autoprovisioning-enabled=false
   - 并且镜像版本（如 v1.25.x）要和你的管理集群版本一致。

3. **部署**：

   codeBash

   ```
   kubectl apply -f cluster-autoscaler-rbac.yaml
   kubectl apply -f cluster-autoscaler-deployment.yaml
   ```

------



### 第五步：创建你的第一个自动化集群

**目标**：真正让 CA (Cluster API) 接管你的 BYOH。

1. **生成集群 YAML**：

   codeBash

   ```
   clusterctl generate cluster my-cool-cluster --flavor vm --kubernetes-version v1.25.5 > my-cluster.yaml
   ```

2. **开启自动伸缩开关**（重要！）：
   打开 my-cluster.yaml，找到 MachineDeployment 这一节，在 annotations 下面加上这两行：

   codeYaml

   ```
   metadata:
     annotations:
       cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
       cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "5" # 你池子里有多少机器就写多少
   ```

3. **应用配置**：

   codeBash

   ```
   kubectl apply -f my-cluster.yaml
   ```

------



### 傻瓜都能懂的原理（它是怎么“对接”的？）

1. **Pod 没地方跑了**：业务集群里突然来了很多流量。
2. **CAS (自动扩缩容组件) 发现了**：它看到 Pod 在排队，于是去把管理集群里的 MachineDeployment 的副本数从 1 改成了 2。
3. **Cluster API (核心) 响应了**：它发现需要多一台机器，于是创建了一个 ByoMachine。
4. **BYOH Controller (大脑插件) 动手了**：它发现有个 ByoMachine 缺身体，于是去 ByoHost（你的闲置池子）里找，发现有一台处于 Available 的空闲机器，立刻把它们俩**绑定**在一起。
5. **Host Agent (主机代理) 接令了**：这台主机发现自己被选中了，二话不说，立刻在本地跑 apt install kubelet 并执行 kubeadm join 自动加入集群。
6. **成功扩容**：几分钟后，新节点就绪，Pod 开始跑了。

**总结一句话：你只管往池子里加机器（跑 Agent），剩下的扩容缩容，全自动。**