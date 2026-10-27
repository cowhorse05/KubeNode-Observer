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

未归属 exec 通过单个有界队列触发校正，解决第一次 CRI 读取早于容器就绪时不能恢复的风险。最多 128 个待处理 cgroup，每项最多 5 次、30s 后过期；刷新合并为全局串行任务，每次完成后至少等待 1s，不为每个事件创建 goroutine。成功解析即删除，长期未知的宿主 cgroup 不会使限频键无限增长。超过队列容量、极短容器在解析前消失或事件环已覆盖时，仍可能 unresolved，不承诺无限历史补全。

## CPU runtime 聚合

```text
sched_switch raw tracepoint
          │ switch-out task
          ▼
 task_start PerCPUArray ──> scheduler runtime delta
          │
          ▼
 ├── cpu_runtime LRU map（8192 条进程明细）
 │   key: cgroup ID + TGID + PID
 └── cgroup_runtime LRU map（1024 条 cgroup 累计量）
     key: cgroup ID
          │ 每秒读取累计值并做差
          ▼
 CPU Collector ──Topology Store──> Pod / Container（cgroup 累计差）
                                 Process（进程明细差）
          │
          ▼
 /api/v1/cpu ──> kno top pods|containers|processes
```

高频 `sched_switch` 事件只在内核中更新 map，用户态每秒读取一次累计值。每 CPU 一个 baseline slot 保存切入任务的 PID 和 `se.sum_exec_runtime`，切出时取调度器累计 runtime 差，不再用两次 tracepoint 的墙钟差近似 CPU 时间。PID 匹配和单 CPU 上的切换序列避免基线依赖 PID LRU 保留；Agent 刚附着时尚未建立基线的首个切片会跳过。

同一 delta 分别累加到 cgroup 总量和进程明细。Pod/Container 不再依赖进程 map 中仍保留哪些 key；进程视图仍按 TGID 汇总线程，受 8192 条明细容量限制，因此不保证下钻列表之和等于容器总量。响应的 `accounting=cgroup|process` 标明来源。cgroup map 本身也有 1024 条工作集上限，不提供无限历史或逐次 eviction 计数。

进程 map 使用 batch lookup 游标，保留最终 `ErrKeyNotExist` 同时返回的有效条目，读取仍非原子；不支持 batch 或遇到错误时显式失败，不静默换回不可靠遍历。生产窗口使用 cgroup 快照读完的时间戳，不把后面的进程扫描耗时误作本次计数的时间点。`cpuPercent = 窗口内 runtime / 实际墙钟窗口 × 100`，多线程工作负载可以超过 100%。

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

`/metrics` 使用 Prometheus text format，只观测 Agent 自身和项目 eBPF map，不重复实现 node-exporter。map stats 由单个后台 worker 首次立即采样，之后每次完成后等待 `--map-stats-interval`（默认 15s）再刷新。占用统计只遍历 key，不读取 value；最多执行 `MaxEntries+1` 次 next-key 调用，无法完成则报错，不输出被截断的计数。并发更新/删除下是非原子的近似 census，可能重复或漏计，不用于精确 eviction/丢失率。

HTTP 抓取只读取不可变快照，不等待或触发 map 扫描。`bpf_map_stats_ready/age_seconds/last_success_timestamp_seconds/refresh_duration_seconds/refresh_error/refresh_errors_total` 描述各 source 的新鲜度和错误；标签固定为 exec/cpu/runqlat。刷新失败可暂时保留最近成功值，同时增加 collection errors；超过 3 倍刷新间隔后该 source 的 entries/capacity/memlock 全部停止输出，不能把缺失当成零。健康/lifecycle/process 指标仍在每次 scrape 时读取。退出时先停止并等待后台刷新，再关闭 map FD。

## 内存诊断与 BTF 生命周期

Agent 完成 exec、CPU、runqlat 三组程序的 CO-RE relocation 和 attach 后，调用依赖库的 `btf.FlushKernelSpec()`，释放 Go 全局缓存对内核 BTF 类型图的引用。这不是卸载 BPF 程序，也不关闭 map/program FD；未来再加载程序时，库会按需重新读取 BTF。这里不强制 GC，由 Go 的正常回收和 scavenging 释放内存；若以后引入动态 probe，应重新评估加载频率与缓存生命周期。

仅在显式启用本地诊断端口时，`/diagnostics` 才额外输出 Agent 自身 cgroup 的 `memory.current/max/peak/stat/events`、RSS、Go heap/stack、`Sys-HeapReleased` 和当前 GOMEMLIMIT。内存读取错误与 CPU 诊断错误分别报告，缺失值不伪装成零。`memory.peak` 是 cgroup 创建以来的高水位；不支持时省略。

RSS、Go managed memory 和 cgroup charge 是不同口径，不能互相替代；kernel/slab 有包含关系，也不能把 map memlock 再加到 cgroup current。GOMEMLIMIT 是 Go runtime 的软限制，不覆盖所有进程和内核内存。默认部署不设置该变量；实验可显式对照 `original,80MiB`，但有限窗口没有 OOM 不等于长期预算有保证。操作方法见 [内存预算观测](experiments.md#内存预算观测)。
