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

## 精度与一致性

- CPU 使用率是最近约 1 秒窗口内的 runtime/wall time，不是长期平均值；多线程 workload 可以超过 100%；
- runqlat 的 P50/P95/P99 是微秒 log2 bucket 上界，不是精确分位数；
- runqlat 起止 map 快照不是跨 map 原子事务，事件总数以 histogram delta 为准；
- 同一时间只执行一个 runqlat 请求，避免大快照并发放大内存；
- CRI 每 5 秒全量校正，短生命周期或刚创建容器可能短暂显示 unresolved；
- 删除后的 cgroup 身份只保留 5 分钟 tombstone；生命周期事件内存环最多 4,096 条且不持久化；
- LRU map 到达容量会淘汰旧 key，v0.1.0 不提供逐次淘汰计数。

## 资源与安全

- Agent 使用 privileged、hostPID、hostNetwork，并挂载 host procfs、cgroup v2、tracefs 和 containerd socket；只应部署到受信任的开发/研究集群；
- HTTP API 当前无 TLS 和鉴权，Service 不应直接暴露到不可信网络；
- 128 MiB memory limit 下，40,000 个短进程 churn 后继续运行完整 integration 无重启，CRI working set 约 101.6 MiB；
- BPF map memlock 实测约 7.0 MiB；CPU task/runtime map 均限制为 8,192 entries；
- `/metrics` 对 hash/LRU map 做线性迭代，适合低频 scrape，不建议高频抓取。

## 性能结论边界

当前 off/on 基准运行在共享的 WSL2/Minikube 2 vCPU 环境。CPU map 稳定性修复后的 5 轮中位数吞吐下降 3.67%，P99 均为 26 us，Agent 用户态 CPU 为 2.353%。该结果是下一阶段的 baseline，不是生产环境性能承诺。

下一阶段的优化顺序：

1. 分解 CPU runtime map、runqlat map、拓扑刷新和 metrics scrape 的独立成本；
2. 为 RSS 增长、map occupancy/eviction 和采集耗时增加更细指标；
3. 采用更多轮次及 AB/BA 交替顺序减少热状态和虚拟机噪声；
4. 在保持归属正确性和集成回归通过的前提下调整容量与采样周期。
