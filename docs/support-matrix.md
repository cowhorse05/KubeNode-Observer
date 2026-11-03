# 支持矩阵与已知限制

## v0.1.0 支持矩阵

| 维度 | 已验证/支持 | 尚未支持 |
| --- | --- | --- |
| 架构 | Linux x86_64 | arm64、多架构发布物 |
| 内核 | 6.6 WSL2，BTF、tracefs | 无 BTF 内核 |
| cgroup | v2 | v1、混合层级 |
| Kubernetes | Minikube 1.38.1 / Kubernetes 1.35.1 | 托管集群、多节点汇总 |
| CRI | containerd 2.2.1 | CRI-O、Docker shim |
| Runtime | runc 普通容器 | Kata、microVM、Guest process |
| 观测 | exec/exit、CPU runtime、runqlat | 网络、文件、完整块 IO |
| 输出 | 节点 HTTP API、CLI、Prometheus text | TLS、鉴权、中心化存储、Web UI |

版本表表示实测边界，不代表代码主动拒绝相邻版本。换内核、Kubernetes 或 runtime 后，应先运行 `make check` 和完整 integration。

独立研究扩展（不扩张以上 runc v0.1.0 指标语义）：已在同一 WSL2/Docker-driver Minikube 验证 Kata 4.3.1 **Go/QEMU**、Guest Linux 6.18.56、双容器共享 VM、Host shim/VMM 身份、官方 Monitor 与生命周期边界。尚未验证 runtime-rs/Cloud Hypervisor、Guest 进程探针、VMM/vCPU eBPF 资源视图或多 VM 容量。Go runtime 在上游 4.x 已弃用，选择与限制见 [Kata 指南](kata.md)。

## 精度与一致性

- CPU 使用率是最近约 1 秒窗口内的 runtime/wall time，不是长期平均值；多线程 workload 可以超过 100%；Pod/Container 使用独立 cgroup 累计量，Process 使用有限进程明细，二者不保证求和相等；
- runqlat 的 P50/P95/P99 是微秒 log2 bucket 上界，不是精确分位数；
- runqlat 起止 map 快照不是跨 map 原子事务，事件总数以 histogram delta 为准；
- 同一时间只执行一个 runqlat 请求，避免大快照并发放大内存；
- CRI 每 5 秒全量校正，短生命周期或刚创建容器可能短暂显示 unresolved；
- 删除后的 cgroup 身份只保留 5 分钟 tombstone；生命周期事件内存环最多 4,096 条且不持久化；
- LRU map 到达容量会淘汰旧 key，v0.1.0 不提供逐次淘汰计数。

## 资源与安全

- Agent 使用 privileged、hostPID、hostNetwork，并挂载 host procfs、cgroup v2、tracefs 和 containerd socket；只应部署到受信任的开发/研究集群；
- HTTP API 当前无 TLS 和鉴权，Service 不应直接暴露到不可信网络；
- 历史版本 128 MiB limit 下 40000 短进程回归无重启、CRI working set 约 101.6 MiB，仅作历史记录；当前版本的独立 1816s debug-off 观察完成 93 Pods /186000 子进程，末点 cgroup current 26.98 MiB、启动以来 peak 80.36 MiB，无重启/OOM，不代表长期生产保证；
- 旧版 BPF map memlock 实测约 7.0 MiB；当前 CPU baseline 为每 CPU 一个 slot，进程明细 8192 entries，cgroup 累计量 1024 entries；cgroup 工作集超过容量仍可能淘汰/重置，不保证无限历史；
- `/metrics` 从后台快照读取 map 占用，默认每次刷新完成后等待 15s；并发 census 是近似值，需结合 ready/age/error 判断有效性，过期值省略；
- CPU 进程读取要求内核支持 hash/LRU batch lookup，当前只在表中内核验证；尚未支持的内核明确报错，不提供未经验证的降级路径。

## 性能结论边界

早期顺序 off/on 基准运行在共享的 WSL2/Minikube 2 vCPU 环境，5 轮中位数吞吐下降 3.67%、P99 为 26 us、Agent 用户态 CPU 为 2.353%，仅保留为历史记录。后续 12 CPU 环境的配对实验、metrics 优化和 BTF 内存诊断见实验指南及各日期报告，不混用不同版本、资源和顺序实验作性能承诺。

后续验收与扩展顺序：

1. 用已实现的成本分解和配对工具复测，不把 diagnostics 当业务开销基准；
2. 对照调度器 runtime、cgroup 总量与有限进程明细，保留非原子读取和容量边界；
3. debug-off 的 Pod/进程有限稳定性观察已完成，内存、身份、重启和事件丢失证据见 [收尾报告](../log/2026-10-26-cpu-accounting.md)，不作无限期保证；
4. runc 收尾后进入 Kata 环境 Go/No-Go 与 Host/VMM 身份归属；不以扩充指标替代推进功能。
