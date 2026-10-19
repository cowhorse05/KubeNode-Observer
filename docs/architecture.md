# KubeNode-Observer 架构

## 节点拓扑数据流

```text
Kubernetes API ──Informer──┐
                           │ Pod UID / Phase / QoS 校验
containerd CRI ────────────┼──> Topology Store ──> /api/v1/topology ──> kno topology
  Pod labels / Container   │
  init PID                 │
                           │
/host/proc + cgroup v2 ────┘
  cgroup path / inode / processes

eBPF bpf_get_current_cgroup_id() ──> cgroup ID ──> Topology Store
```

Kubernetes API 与 CRI 解决的是“声明身份”，procfs 与 cgroup 解决的是“当前内核身份”。两者不能互相替代：Pod 可能已删除但 containerd 尚未完成清理，也可能在 Informer 同步前已经启动。当前实现以 CRI 中的运行中容器为拓扑主集合，用 Informer 校验 Pod 身份，再以 init PID 的 `/proc/<pid>/cgroup` 确认内核归属。

## cgroup ID

cgroup v2 下，`bpf_get_current_cgroup_id()` 返回的 ID 可与 cgroup 目录 inode 对齐。Agent 将容器 init PID 映射到 cgroup path，再读取 `/sys/fs/cgroup/<path>` 的 inode。这提供了 eBPF 事件与 Kubernetes Workload 之间不依赖进程名或 PID 猜测的连接键。

## 一致性策略

- CRI 每 5 秒全量校正运行中的容器；
- Informer 持续 watch 当前节点的 Pod 和 Node；
- Pod UID 不一致时保留 CRI 记录，但在 `issues` 中显式报告；
- 容器退出后不再出现在下一次 CRI 快照中；
- 被删除的 cgroup 身份保留为 5 分钟 tombstone，用于归属延迟到达或稍后查询的生命周期事件；
- 单个容器的 procfs/cgroup 读取失败不会丢弃整个节点快照。

## 进程生命周期事件流

```text
sched_process_exec / sched_process_exit
          │
          ▼
  eBPF ring buffer ──丢失计数──> /healthz eventsLost
          │
          ▼
  Event Store（4096 条内存环）
          │ cgroup ID
          ├──> 当前 Topology Store
          ├──> 5 分钟 tombstone
          └──> 未命中时限频触发拓扑刷新
          │
          ▼
 /api/v1/events ──> kno events
```

事件先以稳定的 cgroup ID 入库，查询时会再次尝试补全尚未命中的身份。这使短生命周期进程不依赖 `/proc/<pid>` 在事件消费时仍然存在。API 支持按 namespace、Pod 和事件类型过滤；内存环只用于短期诊断，不承担持久化职责。

当前实现首先保证身份正确性和可验证性。大规模节点上的刷新开销、事件驱动增量更新和 tombstone 容量将在基准测试后继续优化。

## CPU runtime 聚合

```text
sched_switch raw tracepoint
          │ switch-out task
          ▼
 task_start LRU map ──> runtime delta
          │
          ▼
 cpu_runtime LRU map
 key: cgroup ID + TGID + PID
 value: cumulative runtime_ns + comm
          │ 每秒读取累计值并做差
          ▼
 CPU Collector ──Topology Store──> Pod / Container / Process
          │
          ▼
 /api/v1/cpu ──> kno top pods|containers|processes
```

高频 `sched_switch` 事件只在内核中更新 map，用户态每秒读取一次累计值。进程视图会把同一 TGID 的线程 runtime 相加，容器和 Pod 视图继续逐层汇总。`cpuPercent = 窗口内 runtime / 实际墙钟窗口 × 100`，因此多线程工作负载可以超过 100%。

当前窗口是 Agent 最近一次约 1 秒的采样，不是长期平均值。误差主要来自窗口边界上的任务创建/退出、BPF map 并发读取、Agent 自身调度延迟和短暂的元数据未命中；collector 使用实际读取时间而非 ticker 的预定时间作为分母，避免 Agent 被限流时放大结果。

## Run queue latency

```text
sched_wakeup / sched_wakeup_new ──> runnable_since[PID] = timestamp
                                      ▲
sched_switch（prev 仍 runnable）───────┘
          │ next task 真正获得 CPU
          ▼
 latency = switch-in - runnable_since
          │
          ├──> latency_stats[cgroup ID, TGID, PID]
          └──> latency_histogram[..., log2(latency_us)]
                         │ 按需读取起止快照并做差
                         ▼
 /api/v1/runqlat ──> kno trace runqlat
```

任务从睡眠被唤醒时记录 runnable 起点；时间片结束但仍可运行的任务，则在 switch-out 时重新进入等待。下一次 switch-in 结算等待时长。因为 `bpf_get_current_cgroup_id()` 在 `sched_switch` 中只能可靠表示 prev task，程序另外维护 PID → cgroup/TGID 身份缓存，首次尚未建立身份的调度样本会被跳过。

每次等待只更新内核 LRU map，不经过 ring buffer。直方图以微秒为单位，bucket `n` 表示 `[2^n, 2^(n+1)-1]`；P50/P95/P99 返回命中 bucket 的上界，属于有界近似值。API 在请求开始与结束时读取累计 map 并做差，因此 `--duration` 是独立的按需实验窗口。

## Observer 自监控

```text
/proc/self/task/*/schedstat ──> process CPU seconds ┐
/proc/self/statm            ──> process RSS         │
Go runtime                  ──> heap bytes          ├──> /metrics
BPF_OBJ_GET_INFO_BY_FD      ──> map capacity/memlock│
map iterator                ──> logical entries     │
exec event_stats            ──> events lost         ┘
```

`/metrics` 使用 Prometheus text format，只观测 Agent 自身和项目 eBPF map，不重复实现 node-exporter。map entries 在 scrape 时线性遍历，因此定位为低频诊断指标；具体开销必须和 scrape 周期一起解释。
