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
