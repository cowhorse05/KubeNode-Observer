# M6 长时内存稳定性实验

日期：2026-10-19

## 问题

发布审计时，旧 Agent 在 128 MiB limit 下运行约 3 小时 45 分钟后被 OOMKilled。重启前 workload 测试均短时通过，因此仅依赖 smoke test 无法发现该问题。

重启两小时后的 map occupancy 已达到：CPU runtime 24,747、runqlat task identity 15,487、latency stats 3,577、histogram 15,702。CPU collector 每秒将整个 runtime map 复制到用户态，退出进程持续积累会同时增加快照 slice、previous map 和 GC 峰值。

## 修复

- `cpu_task_start`: 32,768 → 8,192；
- `cpu_runtime`: 65,536 → 8,192；
- `/metrics` 新增此前遗漏的 `cpu_task_start` occupancy/capacity/memlock；
- 新增 `make stability`，连续创建 40,000 个短进程后检查 Agent restart、events lost 和 CPU map 容量平台。

8,192 仍远高于当前单节点同时活跃任务数；LRU 会保留最近退出任务，使 1 秒 collector 仍能读取短生命周期 CPU runtime。

## 压力结果

```text
process churn round 1: 20,000 child processes completed
process churn round 2: 20,000 child processes completed
restarts: 0
eventsLost: 0
cpu_runtime: 7,290 / 8,192
cpu_task_start: 7,427 / 8,192
```

随后不重启 Agent，继续运行 lifecycle、CPU hotspot 和 runqlat 完整 integration，三条链路全部通过。最终状态：

```text
restart count: 0
eventsLost: 0
CRI workingSetBytes: 106,524,672
CRI availableBytes: 27,693,056
CRI rssBytes: 97,075,200
cpu_runtime: 7,529 / 8,192
cpu_task_start: 7,158 / 8,192
BPF map memlock total: 7,364,896
```

## Review

- 旧 OOM 根因不是 BPF map 继续分配内核内存（LRU map 预分配），而是 map entries 增长后用户态每秒快照和 GC 峰值变大；
- 进程 `/proc/self/statm` RSS 会包含与容器 cgroup 统计口径不同的驻留页，是否触发 limit 应以 CRI/cgroup working set 与 restart state 为准；
- 高 churn 后再执行 runqlat 会同时触发接近满容量的两次 histogram 快照，是比空集群 smoke 更强的峰值验证；
- 当前证据证明容量平台后的短时稳定性，不替代 24 小时 soak；后续性能阶段应增加长期自动采样和 map eviction 指标。
