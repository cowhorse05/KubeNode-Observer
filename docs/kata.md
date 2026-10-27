# Kata Host 接入（M7）

本阶段提供：在现有 Minikube Docker-driver 节点上运行真实 Kata QEMU Pod，将 CRI Sandbox 关联到 Host shim/VMM，并按 Sandbox 查询官方 Kata Monitor。普通 Pod 仍使用 runc。

不包含自研运行时、Guest eBPF Probe、vCPU thread 调度分析或 runc/Kata 性能优劣结论。Kata Monitor 已有 Guest OS/Agent 聚合指标，不等于项目实现了 Guest 进程观测。

## 为什么 Sandbox 独立建模

一个 Kata Pod 的多个容器共享 VM。CRI 返回的 container PID **不能直接当作宿主容器 init PID**；本环境实际返回的是 VMM PID。继续套用 runc 的 PID→cgroup 逻辑，会把 VMM 重复记在不同业务容器上。

| 字段 | 含义 |
| --- | --- |
| `container.runtimeType` / `runtimeHandler` | CRI 实际运行时及 handler，不仅凭 Pod 的 RuntimeClass 声明猜测 |
| `container.processVisibility` | `host` 或 `guest-unavailable`，后者指本项目 Host 进程探针看不到 Guest 内进程 |
| `snapshot.sandboxes[].id` | Ready Kata Sandbox 的完整 ID；每台 Pod VM 只出现一次，应用重启间隙仍保留 |
| `sandbox.shim` / `hypervisor` | 经核验的 Host PID、启动 tick、真实 cgroup path/inode；缺失或歧义进入 `issues` |

发现链路：CRI Ready Sandbox → 宿主 `/proc` 中可执行文件为 Kata shim、`-namespace=k8s.io` 且 `-id` 完全匹配 → 唯一 QEMU 子孙进程 → 再校验 `/proc/<pid>/stat` 启动时间及 cgroup。不会用短 ID、进程 comm 或 Pod 名称直接猜 VMM，也不读取 Kata 私有 persist.json 格式。

Kata 容器的 Host `pid=0`、cgroup 字段缺省、`processes=[]`。Sandbox 的 VMM cgroup **不加入普通容器的 Resolve 索引**。因此现有 `top containers/processes` 中 Kata 没有结果是“尚无该层观测”，不是测得 Guest CPU 为零。后续 M8 才建立独立 Host/VMM 资源视图，不能用两个容器各一份 VMM 数据来冒充下钻。

## 环境与版本选择

当前验证对象为 WSL2 Linux 6.6、x86_64、cgroup v2、Minikube Docker driver、containerd 2.2.1 的单节点开发环境。安装器只接受该类节点和现有 `version=2` containerd 配置，不是通用的集群安装工具。

固定使用官方 Kata **4.3.1 Go/QEMU** 包，便于先复用可核对的 shim/Monitor 链路。上游 4.x 推荐默认 runtime-rs，Go runtime 已弃用但仍随包提供；本次没有验证 runtime-rs/Cloud Hypervisor，不将识别代码的候选支持当作实测兼容性。正式采用其他后端需重新做运行时、metrics 和生命周期验收。参见 [官方安装说明](https://github.com/kata-containers/kata-containers/blob/4.3.1/docs/installation.md)。

## 安装与复现

依赖 Linux/WSL 的 Docker、kubectl、Minikube、Go、jq、zstd，以及节点内 Python 3。仅对受信任的个人开发节点操作：安装会重启其 containerd。不要在公司共享/生产节点执行。

```bash
make kata-check

# 官方发布资产，不经过 Docker Hub；也可把该文件从其他机器拷贝过来。
mkdir -p dist/kata-4.3.1
curl -fL --retry 2 -o dist/kata-4.3.1/kata-go-static-4.3.1-amd64.tar.zst \
  https://github.com/kata-containers/kata-containers/releases/download/4.3.1/kata-go-static-4.3.1-amd64.tar.zst

make kata-install
make kata-test
```

安装器强制校验 SHA256 `66b35b06f6b41b56f8606ddf3ed3e5660b0971d1e309d903457c4056a0b9eff0`，与 GitHub release asset digest 对应。约 1.3 GB 包仅放忽略的 `dist/`，不提交到 Git。所有项目 workload/Agent 镜像由 scratch 本地构建、无 registry 域名、经 `minikube image load` 注入，测试 Pod `imagePullPolicy: Never`。

已有 `/opt/kata`、备份、受管配置或同名 RuntimeClass 时，安装器拒绝覆盖。原配置备份为节点内 `/etc/containerd/config.toml.kno-before-kata`；仅增加 import 与专用 handler，不改变 `default_runtime_name=runc`。专用 VM 基础内存 512 MiB，RuntimeClass 显式声明 512 MiB/100m overhead；这是开发配置，不是所有 Kata 负载的通用资源建议。

### Docker 的 /dev/shm 陷阱

空 VM 的 KVM ioctl 和官方 runtime check 都通过，仍不保证 Guest 能启动。Docker 节点默认 `/dev/shm` 仅 64 MiB；virtio-fs 的 Guest RAM 可能用它做 backing，触页后会耗尽并导致 QEMU `kvm run failed Bad address`。本项目真实遇到了这一情况，见 [实验报告](../log/2026-10-27-kata-host.md)，上游也有 [同类报告](https://github.com/kata-containers/kata-containers/issues/12919)。

安装器将**该节点 mount namespace** 的 `/dev/shm` 容量调到 1 GiB，并安装 `kno-kata-shm.service` 在 containerd 之前重做该设置，避免重启 Docker 节点后又回到 64 MiB。这是 tmpfs 容量上限，不是预留 1 GiB；节点 memory limit、Observer 200m/128 MiB 均不变。当前只验收一台 Kata VM，增加 VM 数量/Guest 内存前应重新核对 tmpfs、节点预算和调度 overhead，不可照搬此上限。

## 使用

测试脚本会临时为 Agent 添加 `--kata-monitor=http://127.0.0.1:8090`，创建 runc 和双容器 Kata Pod，验证后恢复原 Agent DaemonSet、删除临时 namespace/锁。运行时、RuntimeClass 和 Monitor 保留，便于后续开发；它们不会因为恢复旧 Agent 镜像而卸载。

手动演示时，先部署当前源码构建的 Agent，并显式设置上述参数，然后：

```bash
kubectl --context kno-dev create namespace kno-kata-test
kubectl --context kno-dev apply -f examples/kata-boundary/pods.yaml
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080

./bin/kno topology -n kno-kata-test --pod kno-boundary-kata
./bin/kno kata metrics --sandbox <topology中完整的sandbox-id>
```

`/api/v1/kata/metrics?sandbox=<id>` 只转发官方 `/metrics`。目标必须是显式配置的 HTTP 回环 IP:port，Sandbox 必须在本节点当前拓扑中；不转发任意 URL、redirect、profiling/debug 路径。请求串行、5s 超时、最多 8 MiB。未配置、未知 Sandbox、上游失败分别显式返回 503/404/502，不伪装成空指标。上游单 Sandbox 输出可能不带 `sandbox_id` 标签，查询上下文负责身份，不自行重写指标。官方使用方法见 [Kata Monitor](https://github.com/kata-containers/kata-containers/blob/4.3.1/src/runtime/cmd/kata-monitor/README.md)。

普通 `/metrics` 仍只输出本项目自监控，不自动抓取/混入所有 Guest 指标。当前 HTTP API 无鉴权，不应对不可信网络公开；回环上游限制不是用户鉴权。

## 配置回滚

先确认所有使用 `kata-qemu` handler 的 Pod/Sandbox 已停止，且 containerd 配置没有其他开发者的新改动。再在原开发节点恢复备份，停止受管服务并重启 containerd；不自动删除二进制或收缩仍可能被使用的 tmpfs：

```bash
kubectl --context kno-dev delete runtimeclass kno-kata-qemu
docker exec kno-dev systemctl disable --now kno-kata-monitor.service kno-kata-shm.service
docker exec kno-dev cp -p /etc/containerd/config.toml.kno-before-kata /etc/containerd/config.toml
docker exec kno-dev systemctl restart containerd
```

`/opt/kata` 和备份保留供恢复/审查；若仅重启已安装的节点，可先 `make kata-check` 检查 KVM、tmpfs 和 handler，不重复执行安装。
