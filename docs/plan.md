# KubeNode-Observer 开发计划

## 1. 项目目标

KubeNode-Observer 是一个运行在 Kubernetes 节点上的 runtime-aware 性能观测工具。

项目首先解决一个明确问题：

> 当节点出现 CPU 热点或调度延迟时，将内核事件准确归属到 Node、Pod、Container 和 Process，并提供可下钻的命令行查询能力。

第一版只支持普通 Linux 容器（containerd + runc）。在基础链路稳定后，再扩展 Kata Containers/microVM 的 Host、VMM、Guest 跨层观测。

## 2. 项目原则

1. 少造轮子：复用 Kubernetes API、CRI、cilium/ebpf、Prometheus 数据格式和 Kata Monitor。
2. eBPF 用于动态内核事件，不重复实现 node-exporter 的静态主机指标。
3. 高频事件在内核侧聚合，避免把每次调度事件全部发送到用户态。
4. 每个里程碑都必须产生可运行、可验证的结果。
5. 先完成 runc 的单内核观测闭环，再进入 microVM。
6. CLI 优先，不在早期开发 Web UI、Grafana 大屏和复杂存储。

## 3. MVP 范围

MVP 必须支持：

- 以 DaemonSet 运行在 Minikube 节点。
- 通过 Kubernetes Informer 获取 Pod 元数据。
- 通过 CRI 获取 PodSandbox、Container 和运行时信息。
- 建立 `cgroup ID -> Container -> Pod` 映射。
- 使用 eBPF 采集进程 exec/exit 事件。
- 使用 eBPF 按 cgroup 和进程聚合 CPU runtime。
- 计算进程或容器的 run queue latency。
- 通过 CLI 查看节点热点 Pod、Container 和 Process。
- 暴露 Observer 自身的 CPU、内存、事件丢失等运行指标。

MVP 暂不包含：

- Agent/LLM。
- 多集群控制面。
- Web UI。
- MySQL、Redis 或长期时序数据存储。
- 自动根因分析。
- 网络、文件系统和块 IO 的完整观测。
- 自研容器运行时、CRI shim 或 hypervisor。
- Kata Guest Probe。

## 4. 技术基线

### 4.1 开发环境

- Linux x86_64。
- 支持 BTF 的 Linux 内核。
- cgroup v2。
- Minikube。
- containerd + runc。
- Go。
- clang/llvm。
- libbpf headers。

开发初期先固定版本，避免同时兼容多个内核和 Kubernetes 版本。版本信息必须记录在测试报告中。

### 4.2 主要依赖

- `github.com/cilium/ebpf`：加载和管理 eBPF 程序。
- `bpf2go`：生成 Go bindings。
- `client-go`：Kubernetes Informer 和资源查询。
- Kubernetes CRI gRPC API：查询 PodSandbox 和 Container。
- Cobra：CLI。
- Prometheus Go client：仅暴露 Observer 自身及聚合后的指标。

### 4.3 公司内网镜像约束

公司网络无法直接拉取 Docker Hub 镜像。公司环境配置了默认镜像仓库路由，镜像引用不携带 registry 域名：

- Go builder：`library/golang:1.26-alpine`
- Alpine：`library/alpine:3.20`
- 测试 BusyBox：`iclaw/busybox:1.37.0-musl`

Minikube 使用 `bridge` CNI，避免启动阶段额外拉取 Docker Hub 的 `kindnetd`。个人电脑开发时可以通过 Docker build argument 替换 builder 镜像。

## 5. 目标工程结构

```text
cmd/
  kno-agent/              # 节点 DaemonSet 进程
  kno/                    # 命令行客户端

bpf/
  include/
  exec.bpf.c              # exec/exit 生命周期事件
  cpu.bpf.c               # CPU runtime 聚合
  runqlat.bpf.c           # 调度等待延迟

internal/
  bpf/                    # eBPF 加载、map 读取、ring buffer
  cgroup/                 # cgroup v2 解析与缓存
  cri/                    # containerd CRI resolver
  kubernetes/             # Pod informer 与 owner 元数据
  topology/               # Node/Pod/Container/Process 关联模型
  collector/              # 周期读取和聚合
  api/                    # Agent 查询接口
  metrics/                # Observer 自身指标

api/
  observer.proto          # Agent/CLI 查询协议

deploy/
  helm/
  minikube/

examples/
  cpu-hog/
  cpu-contention/

test/
  integration/
  benchmark/
```

## 6. 核心数据模型

```go
type WorkloadIdentity struct {
    NodeName      string
    Namespace     string
    PodName       string
    PodUID        string
    ContainerName string
    ContainerID   string
    Runtime       string
    CgroupID      uint64
    PID           uint32
    TGID          uint32
}
```

映射链路：

```text
eBPF event
  -> cgroup_id / PID
  -> cgroup resolver
  -> CRI container metadata
  -> Kubernetes Pod metadata
  -> WorkloadIdentity
```

缓存必须处理以下变化：

- Pod 创建和删除。
- Container 重启导致 Container ID 变化。
- 短生命周期进程在元数据补全前退出。
- cgroup ID 被回收和复用。
- Agent 重启后的全量同步。

## 7. 里程碑

### M0：环境与可行性验证

目标：确认 Minikube 节点能够加载项目自己的 eBPF 程序。

任务：

- 建立开发环境检查脚本。
- 检查内核版本、BTF、cgroup v2、clang 和 bpftool。
- 创建 Minikube 集群并确认 container runtime。
- 运行最小 tracepoint eBPF 程序。
- 将最小程序打包为 privileged DaemonSet。
- 记录宿主机、Minikube、内核和 containerd 版本。

完成标准：

- DaemonSet 能在节点成功加载 eBPF 程序。
- 创建测试 Pod 后能够收到至少一种可验证的内核事件。
- 环境可以通过脚本重复创建。

### M1：项目骨架与本地 API

目标：建立可持续开发的 Go 工程骨架。

任务：

- 初始化 Go module。
- 建立 `kno-agent` 和 `kno` 两个二进制入口。
- 建立配置、日志、context 和优雅退出机制。
- 定义 Agent 查询 API。
- 添加 Makefile、lint、unit test 和容器镜像构建。
- 添加基础 CI。

完成标准：

- `make build`、`make test` 和 `make image` 可运行。
- CLI 能连接 Agent 并完成 health check。

### M2：Kubernetes、CRI 与 cgroup 拓扑

目标：不依赖 eBPF，先建立正确的资源身份映射。

任务：

- 使用 Informer 缓存 Node 和 Pod。
- 连接 containerd CRI socket。
- 同步 PodSandbox、Container 和 ContainerStatus。
- 解析 cgroup v2 层级。
- 建立 Container ID、Pod UID、cgroup path 和 cgroup ID 的索引。
- 实现缓存增量更新和周期性校正。

完成标准：

- 对任意测试 Pod，可以列出其 Container ID、cgroup path 和进程列表。
- Pod 删除或重启后，旧映射能够被清理。
- 映射结果可以与 `crictl`、`kubectl` 和 `/proc/<pid>/cgroup` 相互验证。

### M3：进程生命周期观测

目标：打通第一条完整的 eBPF 事件归属链。

任务：

- 实现 exec/exit tracepoint 程序。
- 事件包含 PID、TGID、PPID、comm、cgroup ID 和时间戳。
- 使用 ring buffer 发送低频事件。
- 用户态完成 WorkloadIdentity 补全。
- 处理短生命周期进程和元数据未命中。
- 增加事件丢失计数。

完成标准：

- 在 Pod 中执行命令时，CLI 能显示正确的 namespace、Pod、Container 和 Process。
- 不输出其他 Pod 的错误归属结果。
- 有针对短生命周期进程的集成测试。

### M4：CPU runtime 与热点定位

目标：实现第一个实际可用的性能功能。

任务：

- 基于 `sched_switch` 计算进程 CPU runtime。
- 使用 BPF map 按 cgroup ID、TGID 或 PID 聚合。
- 用户态周期读取并计算 CPU 使用率。
- 支持 Node -> Pod -> Container -> Process 逐层汇总。
- 实现热点排序和时间窗口。

CLI：

```bash
kno top pods
kno top containers -n default -p demo
kno top processes -n default -p demo
```

完成标准：

- CPU hog Pod 能稳定出现在热点列表首位。
- 聚合结果与 cgroup CPU 统计、`pidstat` 或 `top` 的趋势一致。
- 可以说明采样窗口、计算方法和误差来源。

### M5：Run Queue Latency

目标：区分“正在消耗 CPU”和“想运行但得不到 CPU”。

任务：

- 使用 `sched_wakeup`/`sched_wakeup_new` 记录 runnable 时间。
- 在 `sched_switch` 中计算等待时长。
- 以内核侧直方图聚合延迟。
- 输出 P50、P95、P99 或 log2 histogram。
- 增加 CPU contention 示例 workload。

CLI：

```bash
kno trace runqlat --pod demo --duration 30s
```

完成标准：

- CPU contention 场景的 run queue latency 显著高于 baseline。
- 结果可以与 `perf sched` 或同类工具进行对照。
- 高频调度场景下没有不可接受的事件传输量。

### M6：MVP 稳定化与基准测试

目标：形成可以安装、演示和重复测量的 v0.1.0。

任务：

- Helm chart 或完整 Minikube 部署脚本。
- CPU hog 和 CPU contention 示例。
- 单元测试和集成测试。
- 测量 Agent CPU、RSS、BPF map 占用和事件丢失。
- 测量开启/关闭 Observer 时 workload 吞吐和延迟变化。
- 编写架构文档、限制说明和演示文档。

完成标准：

- 新环境能够按照 README 完成安装和演示。
- 所有实验都能由脚本重复执行。
- 发布 v0.1.0。

## 8. microVM 扩展里程碑

microVM 工作在 MVP 完成后开始。

### M7：Kata 环境验证与 Host 侧识别

- 使用 Minikube + containerd 部署 Kata QEMU 或 Cloud Hypervisor。
- 通过 RuntimeClass 创建 Kata Pod。
- 识别 Kata runtime handler、Sandbox ID、shim PID 和 hypervisor PID。
- 接入 Kata Monitor，而不是重写 Kata 指标采集。
- 展示 Host 侧只能看到 VMM、无法看到 guest process 的可见性边界。

### M8：Host/VMM 资源归属

- 将 hypervisor CPU、内存、线程和 IO 归属到 Pod。
- 识别 hypervisor vCPU threads。
- 观测 vCPU thread 的 CPU runtime 和 run queue latency。
- 输出普通容器与 Kata Pod 的统一视图。

### M9：Guest Probe

- 在 Kata VM 内运行最小 Guest Probe。
- 采集 guest process CPU runtime 与 run queue latency。
- 使用 Pod UID 和 Sandbox ID 关联 Host/Guest 数据。
- 先通过 Pod 网络传输聚合数据，VSOCK 作为后续实验。

### M10：跨层时间对齐与延迟分析

- 估算 Host 与 Guest 单调时钟偏移。
- 对齐两层事件时间线。
- 区分 Guest contention 与 Host vCPU contention。
- 构建可重复的 runc/Kata 对照实验。

## 9. 测试策略

### 9.1 单元测试

- cgroup path 解析。
- Container ID 规范化。
- CRI/Kubernetes 元数据合并。
- 缓存创建、更新、过期和冲突处理。
- CPU 百分比与 histogram 计算。

### 9.2 集成测试

- 创建 Pod 后能建立完整映射。
- Container 重启后映射更新。
- Pod 删除后缓存清理。
- exec/exit 事件归属正确。
- CPU hog 排序正确。
- contention 场景的 run queue latency 可检测。

### 9.3 性能测试

- Observer 空闲开销。
- 不同进程创建速率下的事件丢失。
- 不同 map 读取周期下的精度和开销。
- 不同并发 Pod 数量下的缓存和采集开销。
- Observer 开启前后的 workload throughput 和 P99 latency。

## 10. 近期任务清单

只安排当前最近的一段工作，完成后再细化后续任务。

1. 确定开发所使用的 Linux/Minikube 环境。
2. 初始化 Go module 和基础目录。
3. 编写环境检查脚本。
4. 完成最小 tracepoint eBPF 程序。
5. 使用 `bpf2go` 从 Go 加载程序并读取事件。
6. 打包成 Minikube DaemonSet。
7. 添加 `kno` CLI health check。
8. 建立第一条端到端集成测试。

在上述任务完成前，不开始 CRI 映射、CPU profiler、Kata 或 Web UI。

## 11. 主要风险

### 环境风险

- Windows 开发机无法直接提供目标 Linux 内核能力。
- Minikube driver 可能限制 BPF、BTF 或 privileged 容器。
- Kata 需要 KVM 或 nested virtualization。

应对：将 M0 设为独立的 Go/No-Go 里程碑，先验证环境，再写大量业务代码。

### eBPF 可移植性

- 不同内核的 tracepoint、BTF 和 struct 定义可能不同。

应对：初期固定内核版本，使用 CO-RE，并明确支持矩阵。

### 元数据竞态

- eBPF 事件可能早于 Kubernetes/CRI 缓存更新。
- 短生命周期进程可能在查询 `/proc` 前退出。

应对：事件中尽量携带稳定标识，设计短期 pending queue 和缓存校正机制。

### 范围膨胀

- eBPF、Kubernetes、Kata 任意一个方向都可以无限扩展。

应对：v0.1.0 只交付 exec/exit、CPU runtime、run queue latency 和 runc 归属链。

## 12. 发布节点

- `v0.0.1`：Minikube 中加载 eBPF 并读取事件。
- `v0.0.2`：Pod/Container/cgroup 拓扑查询。
- `v0.0.3`：exec/exit 事件完成 Kubernetes 归属。
- `v0.1.0`：CPU hotspot + run queue latency，runc MVP。
- `v0.2.0`：Kata Host/VMM 观测。
- `v0.3.0`：Kata Guest Probe 与跨层观测。
