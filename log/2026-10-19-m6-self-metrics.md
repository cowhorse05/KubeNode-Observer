# M6 Observer 自身指标实验记录

日期：2026-10-19

## 目标

在进入性能优化前建立 Observer 自身的测量基线，暴露进程 CPU、RSS、Go heap、生命周期事件计数以及每张 eBPF map 的容量、逻辑条目数和内核报告的 memlock。

## 实现

- `/metrics` 使用 Prometheus text exposition format，不依赖外部采集服务；
- CPU 时间汇总 `/proc/self/task/*/schedstat`，RSS 读取 `/proc/self/statm`；
- BPF map 容量和 memlock 来自 `BPF_OBJ_GET_INFO_BY_FD`；
- hash/LRU/array map 的当前条目通过迭代器统计；ring buffer 不存在逻辑条目概念，因此固定报告 0；
- metric scrape 失败只增加当次 `kno_agent_metrics_collection_errors`，不影响健康与诊断 API。

## Minikube 实测

环境与 M5 相同，重新构建并加载 `kubenode-observer/kno-agent:dev` 后，DaemonSet Ready `1/1`、restart count 为 0。首次 scrape 的关键数据：

```text
kno_agent_cpu_seconds_total                 monotonic process total
kno_agent_resident_memory_bytes             114,618,368
kno_agent_events_lost_total                 0
kno_agent_metrics_collection_errors         0
```

主要 eBPF map：

| Map | Current entries | Max entries | Memlock bytes |
| --- | ---: | ---: | ---: |
| `exec_events` (ring buffer) | 0 | 1,048,576 bytes | 1,065,368 |
| `event_stats` | 1 | 1 | 392 |
| `cpu_runtime` | 614 | 65,536 | 6,817,024 |
| `runqlat_runnable_since` | 1 | 16,384 | 1,312,000 |
| `runqlat_task_identity` | 605 | 16,384 | 1,443,072 |
| `runqlat_latency_stats` | 510 | 4,096 | 460,032 |
| `runqlat_latency_histogram` | 2,198 | 16,384 | 1,574,144 |

## 阶段 Review

- 指标读取不改变 map 内容，不会干扰 CPU 和 runqlat delta 计算；
- map 标签来自编译期固定名称，不引入高基数；
- CPU 和事件累计值使用 counter，RSS、heap、map 与当次错误数使用 gauge；
- BPF memlock 是内核报告值，比按 key/value 大小推算更可靠，但不等同于容器 working set；
- 当前 map entries 在 scrape 时做线性迭代，适用于低频自监控；scrape 周期对 Agent CPU 的影响需要在后续性能基准中测量。

性能基准首轮发现只读取 `/proc/self/schedstat` 会得到调用线程而非整个 Go 进程的 CPU 时间，因此数值在采样窗口内可能不变。实现已改为汇总所有 task，并用两个伪 task 的单元测试覆盖；修复后的 5 轮 benchmark 测得 Agent 平均 CPU 为 1.674%。
