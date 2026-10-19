# M6 Observer 开销基准

日期：2026-10-19
环境：WSL2 Ubuntu 24.04、Minikube 1.38.1、Kubernetes 1.35.1、2 vCPU、cgroup v2

## 研究问题

开启 exec、CPU runtime 和 run queue latency 三组 eBPF probe 后，固定 CPU workload 的吞吐和尾延迟变化多大？Agent 用户态自身消耗多少 CPU、RSS 和 BPF map 内存？

## 方法

- workload Pod 限制为 1 CPU，启动 8 个锁定 OS thread 的 worker；
- 每次 operation 执行 64 轮 SHA-256 后调用 `sched_yield`，同时覆盖 CPU 工作和高频调度；
- 单轮 8 秒，Observer off/on 各运行 5 轮，取中位数；
- 延迟用 1 us 线性 histogram 统计至 10 ms，避免 log2 桶将 17/33 us 的微小跨桶误报为 94% 跳变；
- off 通过删除 DaemonSet 确保 BPF link 关闭，on 重新部署并等待 Ready；
- Agent CPU 用所有 `/proc/self/task/*/schedstat` 的窗口 delta 计算。

## 结果

| Observer | Throughput (ops/s) | P99 (us) |
| --- | ---: | ---: |
| off round 1 | 166,406.93 | 13 |
| off round 2 | 168,685.35 | 16 |
| off round 3 | 170,227.58 | 15 |
| off round 4 | 165,191.72 | 18 |
| off round 5 | 169,327.03 | 13 |
| **off median** | **168,685.35** | **15** |
| on round 1 | 172,091.30 | 16 |
| on round 2 | 173,114.79 | 15 |
| on round 3 | 153,445.28 | 19 |
| on round 4 | 170,759.40 | 17 |
| on round 5 | 172,380.25 | 16 |
| **on median** | **172,091.30** | **16** |

中位吞吐差异为 `+2.02%`，方向与“开启观测产生加速”不符，应视为当前虚拟化环境的运行噪声，而不是性能收益。P99 从 `15 us` 到 `16 us`，绝对增加 `1 us`（`+6.67%`）。

Observer 自身：

```text
average process CPU: 1.674%
RSS after benchmark: 125,874,176 bytes
BPF map memlock:     12,672,032 bytes
events lost:         0
agent restart count: 0
```

## 阶段 Review

- 初版 CPU 指标只读 main thread，benchmark 显示 0%，已定位并修复为全 task 汇总；
- 初版 latency 使用 log2 桶，17/33 us 的跨桶导致百分比失真，已改为 1 us 线性桶；
- 5 轮中 on round 3 有明显低吞吐，说明 WSL2/Minikube 上单次结果不可作为结论；中位数能抗单个异常点，但毕业设计正式实验仍应增加轮数并交替 AB/BA 顺序；
- 当前数据没有显示可区分于噪声的吞吐下降，但 P99 有 1 us 增量；
- RSS 已接近 128 MiB limit，且 BPF memlock 为 12.1 MiB，这比吞吐差异更值得优先优化；
- 至此已具备性能优化的 baseline。下一阶段应先拆分 map 占用、拓扑/CPU 采集周期和 scrape 迭代成本，再做参数优化，不应根据本轮噪声直接修改 eBPF 逻辑。
