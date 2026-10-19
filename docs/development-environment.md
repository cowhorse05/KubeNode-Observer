# 开发与验证环境

本文记录 M0-M6 验证使用的环境，便于后续在个人电脑和公司集群复现实验。

## 已验证环境

验证日期：2026-10-19

| 组件 | 版本或状态 |
| --- | --- |
| Host | Windows + WSL2 Ubuntu 24.04.1 LTS |
| Kernel | `6.6.114.1-microsoft-standard-WSL2` |
| Go | `1.22.2` |
| clang | `18.1.3` |
| Minikube | `1.38.1` |
| Kubernetes | `1.35.1` |
| containerd | `2.2.1` |
| cgroup | v2 |
| Kernel BTF | `/sys/kernel/btf/vmlinux` 可用 |
| tracefs | `/sys/kernel/tracing` 可用 |

## 验证结果

- `kno-agent` 可在 WSL2 宿主机直接加载 eBPF 程序；
- `kno-agent` DaemonSet 可在 Minikube 节点加载 `sched_process_exec` tracepoint；
- 本地测试 Pod 的 `workload` 进程事件能够经 ring buffer 送达 Go 用户态；
- `/healthz` 能报告 eBPF 加载状态、运行时间与事件总数；
- Agent 与 smoke workload 都可通过本地静态二进制构建，不依赖 Docker Hub 运行时镜像。
- CRI、Kubernetes Informer、procfs 与 cgroup v2 的拓扑结果已完成三方校验；测试 cgroup inode 与 eBPF 事件中的 cgroup ID 一致。
- `sched_process_exec` 与 `sched_process_exit` 可同时加载，CLI 能按 namespace、Pod 和事件类型查询结果；
- 生命周期事件包含 PID、TGID、PPID，并能补全 Pod、Container 和镜像身份；
- 非零退出码验证中，内核原始 wait status `512` 被归一化为 `exitCode=2`，同时保留 `rawExitCode=512`；
- 连续测试期间 `/healthz` 报告 `eventsLost=0`。
- `sched_switch` raw tracepoint 能在 DaemonSet 中加载，调度 runtime 只通过 BPF LRU map 聚合，不经过 ring buffer；
- 单 worker CPU hog 稳定排在 default namespace 热点首位，稳定窗口实测约 `99.48%`，趋势与 cgroup v2 `cpu.stat` 的单核占用一致；
- `kno top pods`、`kno top containers` 和 `kno top processes` 返回相同 runtime 的逐层归属，进程名在 exec 后正确刷新为 `workload`；
- `make cpu-smoke` 会同时验证热点排名、70%-110% 的单 worker 合理区间以及 Container/Process 下钻结果。
- run queue latency 的三个 raw tracepoint 可在当前内核加载，5 秒实验中不向用户态传输逐调度事件；
- 1-worker 单核 baseline 的 P95 为 `31us`，8-worker 同核 contention 的 P95 为 `1023us`（另一轮为 `2047us`），差异超过 30 倍；
- 同一轮 cgroup v2 `cpu.pressure` 的 `some total` 增量分别为 `89510us` 和 `5940045us`，与 runqlat 上升趋势一致；
- Minikube 节点未安装 `perf`，因此自动对照选择 cgroup PSI；`make runqlat-smoke` 会同时验证两种信号。
- `make demo` 已完整通过，并验证卸载后项目资源不存在、重新安装后 Agent Ready `1/1`；
- `/metrics` 实测可读取 Agent 多线程 CPU、RSS、事件丢失和 8 张 eBPF map 的 entries/capacity/memlock；
- 发现旧 CPU map 配置在约 3 小时 45 分钟后触发 128 MiB OOM；将 task/runtime map 从 32,768/65,536 缩至 8,192/8,192；
- 两轮共 40,000 个短进程 churn 后继续运行完整 integration，Agent restart count 与 events lost 均为 0，CRI working set 约 `101.6MiB`；
- 修复后 Observer off/on 各 5 轮基准的吞吐中位数差异为 `-3.67%`，P99 均为 `26us`，Agent 用户态 CPU 为 `2.353%`，BPF map memlock 为 `7.0MiB`。

Minikube 容器需要显式挂载节点的 `/sys/kernel/tracing`。仅设置 `privileged: true` 不会把宿主机 tracefs 自动带入容器的 mount namespace。

## 镜像解析差异

公司环境通过容器运行时配置解析不带 registry 域名的镜像短名称；项目清单遵循这一约定。标准 Minikube 未配置相同 mirror 时会把短名称解析到 Docker Hub，因此本地验证使用 `minikube image load` 注入项目镜像。
