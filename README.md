# KubeNode-Observer

Kubernetes 节点出现热点时，节点级指标只能说明“哪里变慢了”，很难直接回答“哪个 Pod、容器和进程导致了问题”。
KubeNode-Observer 是一个以 Go、eBPF 和 Kubernetes 为核心的节点观测项目，目标是建立 Node → Pod → Container → Process 的关联，并为后续的 microVM/Kata Containers 隔离开销研究提供可复现实验基础。

当前 v0.1.0 已完成进程生命周期、CPU 热点和 run queue latency 观测闭环：节点 DaemonSet 捕获 exec/exit 与调度事件，通过 cgroup ID 关联 Kubernetes Workload，并提供 Pod → Container → Process 逐层下钻。

2026-11-06：Kata 研究扩展进入 M9，独立 Guest Probe 在官方 debug Guest 内核中复用 CPU/runqlat eBPF，通过 Pod 网络与 Node 校验的 Sandbox 身份关联。Host/VMM 与 Guest 记账严格分开；runqlat 显式标注线程覆盖率，尚未做跨层时间对齐。字段、安装和实验见 [M9 指南](docs/guest-probe.md)，Host 视图见 [M8 指南](docs/host-resources.md)，runc 方法与性能限制见 [收尾报告](log/2026-10-26-cpu-accounting.md)。

## 当前能力

- 基于 `cilium/ebpf` 加载 CO-RE eBPF 程序；
- 捕获进程 exec/exit 事件及 PID、TGID、PPID、comm、cgroup ID、退出码；
- 基于 `sched_switch` 在内核侧按 cgroup、TGID 和 PID 累计 CPU runtime，不向用户态传输高频调度事件；
- 基于 `sched_wakeup`、`sched_wakeup_new` 和 `sched_switch` 计算 runnable 等待时间，并在内核侧聚合 log2 直方图；
- 通过 Kubernetes Informer、CRI、procfs 和 cgroup v2 构建并交叉校验 Pod → Container → Process 拓扑快照；
- 使用当前拓扑与短期 tombstone 为事件补全 namespace、Pod、Container 和镜像身份；
- 以特权 DaemonSet 运行，并最小化挂载节点 tracefs；
- 提供 `/healthz`、`/api/v1/topology`、`/api/v1/events`、`/api/v1/cpu`、`/api/v1/runqlat` 以及对应 CLI；
- 统计 ring buffer 预留失败造成的事件丢失，并通过 `eventsLost` 暴露；
- 通过 `/metrics` 暴露 Agent CPU、RSS、Go heap、BPF map 占用和事件丢失；
- 提供不依赖远端镜像仓库的本地端到端 smoke test。

Kata 研究扩展另外提供 `snapshot.sandboxes` 和 `kno kata metrics --sandbox <id>`，不把 CRI 返回的 VMM PID 当作 Guest 容器 init PID；普通 runc 功能保持原语义。

M8 的 `kno top workloads -n <namespace> --duration=5s` / `/api/v1/host/resources` 统一展示 runc container 与 Kata sandbox 的 **Host** CPU、带来源标记的内存，以及 VMM IO/线程/vCPU 等待；缺失结果显式为 `null` 和 `partial`。不要将 VMM RSS 当作 Guest 已用内存。`make m8-test` 在已安装 Kata 的节点上运行 baseline → 同核竞争 → 恢复实验，结束恢复 Agent 并删除测试资源。

M9 的 `kno guest resources --sandbox <完整 ID> --duration=5s` / `/api/v1/guest/resources` 返回独立 **Guest** 进程窗口。默认不启用 Node relay，需专用 RuntimeClass、特权 sidecar、Pod PID namespace 和显式 opt-in。`make guest-check` 验证能力，`make guest-kernel-check` 在 Guest 跑内核测试，`make m9-test` 运行真实负载/边界/清理实验。详见 [Guest Probe 指南](docs/guest-probe.md)。

## 一键 Demo

在满足 Linux BTF、cgroup v2、Docker、kubectl 和 Minikube 前置条件后：

```bash
make demo
```

该命令会构建本地静态二进制和无 registry 域名的镜像、注入 `kno-dev` Minikube、部署 Agent，并依次验证 lifecycle、CPU hotspot 和 run queue latency。它不会从 Docker Hub 拉取项目 workload 镜像。

只安装或卸载项目资源：

```bash
make install-minikube
make uninstall-minikube
```

完整操作步骤见 [Demo 指南](docs/demo.md)，支持边界见 [支持矩阵与限制](docs/support-matrix.md)。

## 本地开发

要求 Linux/WSL2 内核启用 BTF、cgroup v2 和 tracefs，并安装 Go、clang/LLVM、Docker、kubectl 与 Minikube。

```bash
make check
make generate
make test
make build
```

启动开发集群并部署：

```bash
./hack/start-minikube.sh
make image-local
minikube -p kno-dev image load --overwrite=true kubenode-observer/kno-agent:dev
kubectl --context kno-dev apply -f deploy/minikube/kno-agent.yaml
kubectl --context kno-dev -n kube-system rollout status daemonset/kno-agent
```

运行完全本地的端到端验证：

```bash
make smoke
make cpu-smoke
make runqlat-smoke
```

查看 Agent 原始日志：

```bash
kubectl --context kno-dev -n kube-system logs -l app.kubernetes.io/name=kno-agent -f
```

访问健康接口：

```bash
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080
./bin/kno health
curl http://127.0.0.1:18080/metrics
```

BPF map 指标现在由后台默认每 15 秒刷新，抓取不会触发遍历。请同时查看 `kno_agent_bpf_map_stats_ready`、`age_seconds` 和 `refresh_error`；首次未就绪或过期时省略 map 数值，而不是报告为零。可用 `--map-stats-interval` 调整刷新间隔，详见 [架构说明](docs/architecture.md)。

查询指定 Pod 的容器、cgroup 和进程拓扑：

```bash
./bin/kno topology --namespace kube-system --pod coredns-7d764666f9-lfstc
```

查询已归属的进程生命周期事件：

```bash
./bin/kno events --namespace default --pod kno-exec-test-local --type exec --limit 20
./bin/kno events --namespace default --pod kno-exec-test-local --type exit --limit 20
```

`exitCode` 是面向用户的退出码，`rawExitCode` 保留内核 tracepoint 提供的 wait status；因信号退出时还会返回 `exitSignal`。

查看最近一个采样窗口的 CPU 热点：

```bash
./bin/kno top pods --namespace default
./bin/kno top containers --namespace default --pod kno-cpu-hog-local
./bin/kno top processes --namespace default --pod kno-cpu-hog-local
```

`cpuPercent` 表示采样窗口内消耗的 CPU runtime 除以墙钟时间：100% 约等于持续占用一个逻辑 CPU，多线程容器和 Pod 可以超过 100%。`make cpu-smoke` 会构建本地 CPU hog 镜像，并验证它稳定排在热点首位。

Pod/Container 总量使用独立的 cgroup 累计量，Process 使用有限的进程明细，响应中的 `accounting` 标明来源。因此进程列表之和不保证等于容器总量，尤其在大量短进程下；调度器计时方法与容量边界见 [架构说明](docs/architecture.md#cpu-runtime-聚合)。

按需测量 Pod 的 run queue latency：

```bash
./bin/kno trace runqlat --namespace default --pod demo --duration 30s
```

结果包含等待次数、平均值、P50/P95/P99、最大 bucket 和微秒级 log2 直方图。`make runqlat-smoke` 会把 1-worker baseline 与 8-worker 单核 contention 场景进行对照，并同时检查 cgroup CPU pressure 趋势。

拓扑中的 `cgroupId` 是 cgroup v2 目录 inode，可直接与 `bpf_get_current_cgroup_id()` 返回值关联。CRI 负责提供容器 PID 和 Kubernetes 标签，Informer 负责验证 Pod UID、Phase 与 QoS，`/host/proc` 负责确认实际进程归属。

重复运行 Observer off/on 性能基线：

```bash
make benchmark
```

`make benchmark` 现采用 off/on、on/off 交替的配对实验，`make accuracy` 对照 eBPF CPU runtime 与同窗口的 cgroup `cpu.stat`；`make experiment` 连续运行两者。每轮参数、环境、源码快照和原始数据保存在 `log/experiments/`。具体方法见 [配对实验与精度对照](docs/experiments.md)。

`make cost` 单独运行采集成本诊断：steady/churn × Agent 200m/1 CPU，记录分阶段墙钟耗时、CPU profile、分配量和 Agent cgroup 限流。使用独立 `:cost` 镜像，结束恢复原部署；此模式启用额外扫描和 profiling，不作为业务开销基准。

`make metrics-compare` 使用已预加载的旧 `:cost` 和新 `:metrics-cache` 镜像，交替 AB/BA 比较持续 churn、每秒抓取一次 metrics 时的 CPU 和抓取延迟。此模式不开 profile/accuracy 请求，默认每个窗口 30 秒，包含后台刷新成本。

`make memory` 在临时的 128 MiB / 200m Agent 上对照原 GOMEMLIMIT 与 `80MiB`，记录 5 轮 churn 和 idle 的 cgroup 内存、Go heap、RSS、GC 和 heap profile，结束恢复原部署。探针加载后会释放不再需要的内核 BTF 类型缓存；不强制 GC，也不调整生产部署的 GOMEMLIMIT。有限观测不代表长期 OOM 保证，详见 [实验指南](docs/experiments.md#内存预算观测)。

v0.1.0 旧版顺序实验在 2 vCPU WSL2/Minikube 的 5 轮中位数结果为：吞吐差异 `-3.67%`、P99 无变化、Agent 用户态 CPU `2.353%`、RSS 约 `116 MiB`、BPF map memlock `7.0 MiB`。该数据保留作历史记录，不与新的配对差值直接等同；见 [实验记录](log/2026-10-19-m6-observer-benchmark.md)。

## 内网镜像约定

项目中的基础镜像和示例镜像使用不带 registry 域名的短名称，例如 `library/golang:1.26-alpine` 和 `iclaw/busybox:1.37.0-musl`，由公司环境的容器运行时镜像仓库配置完成解析。标准 Minikube 会把短名称补全为 `docker.io/...`；在无法访问 Docker Hub 的个人环境中，优先使用 `Dockerfile.local`、`minikube image load` 和 `make smoke`，它们不需要拉取运行时镜像。

完整的研究问题、里程碑与验收指标见 [开发计划](docs/plan.md)，数据流见 [架构说明](docs/architecture.md)。

