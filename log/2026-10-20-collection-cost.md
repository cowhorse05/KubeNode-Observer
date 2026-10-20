# 2026-10-20 采集成本分解

## 结论

本阶段只建立诊断与证据，不修改 BPF map 容量、读取算法或生产采集周期。8 个 case 均完成，原始数据在 [20261020-collection-cost](experiments/20261020-collection-cost/summary.json)。

churn 的长读耗时在 200m 配额下复现，两次最大精度读耗时分别为 1044.2 ms、504.5 ms；1 CPU 对照为 23.3 ms、53.7 ms。200m 的 Agent cgroup 同时记录明显更多的限流。结果支持“map 读取工作量与 CPU quota/调度延迟共同放大尾延迟”，不能把所有墙钟耗时都当作遍历本身的 CPU 成本。

另一个值得先处理的问题是 `/metrics` 的 map occupancy 统计：churn 中单次 scrape 最慢 2104.5 ms，其中 runqlat 四组 map 的统计为 1400.2 ms。即使 1 CPU，单次 scrape 仍为 93.8–164.9 ms。下一次优化优先考虑降低 metrics 全量遍历频率、异步缓存，并评估库提供的批量读取；保留精度/归属/丢失指标，不能只提高配额后宣布问题解决。

## 实现与环境

- 实现提交：`6836075ad152a3907ae900f5d14c63a6aa0eb1d6`，基于已合入 main 的 PR #1；测量时 tracked worktree 干净。
- Agent 的 `--debug-listen` 默认关闭，只接受 loopback IP。固定标签记录 CPU 生产扫描、accuracy 额外扫描、CPU 聚合、拓扑 CRI/enrichment/refresh、三类 metrics map 统计和 scrape 总时长。无逐事件计时，不新增依赖。
- `/diagnostics` 从进程实际 cgroup 读取 `cpu.stat` / `cpu.max`，另存 Go 累计分配量；CPU pprof 通过独立 loopback HTTP server 暴露。
- `kno-experiment --mode cost` / `make cost` 使用独立短名称镜像 `kubenode-observer/kno-agent:cost`，实际 image ID 为 `sha256:e73f5b8b57113f1a5b4e349088887eb4d3329db8bfbfc7c81dd12635e88debe6`，不覆盖原来的 `:dev`。
- WSL2 + Minikube `kno-dev`，单节点，Kubernetes 1.35.1，内核 6.6.114.1-microsoft-standard-WSL2，Go 1.22.2；节点报告 12 CPU。环境 JSON、镜像 ID、源码归档和 hash 均随原始数据保存。

## 方法

```bash
make image-local IMAGE=kubenode-observer/kno-agent:cost
make experiment-build benchmark-image
minikube -p kno-dev image load --overwrite=true kubenode-observer/kno-agent:cost
minikube -p kno-dev image load --overwrite=true kubenode-observer/observer-benchmark:dev
./bin/kno-experiment --mode cost --context kno-dev --duration=10s \
  --accuracy-repeats=2 --output log/experiments/20261020-collection-cost
```

steady/churn × Agent 200m/1 CPU，每组两次，第二次反转两因子的顺序。每个 case 使用新 Agent，CPU request 固定 100m，memory limit 保留 128 MiB；workload 为同一个 1 CPU limit Pod、8 workers。等待 Ready、settle 5s、确认负载运行并预热 3s 后，同时采集 10s CPU profile 和 250ms accuracy 对照，在中部抓取一次 `/metrics`。结束时确认负载仍运行，等待有限负载完成，校验 Agent 同 UID、Ready、零重启。

整个矩阵期间不运行编译、测试和 profile 解析。所有负载结束后才生成 `*-cpu-top.txt`；四个 churn profile 另生成 `*-cpu-cumulative-top.txt`，可用 `go tool pprof -top -cum -nodecount=50 <profile>` 复核。

## 原始结果摘要

以下 CPU 为 Agent cgroup 在诊断前后快照间的 `usage_usec` 差；读耗时包含前后 `cpu.stat` 读取，不仅是 `CPUProbe.Snapshot`。分配量为 `TotalAlloc` 差，不是 RSS 增长。

| 顺序 | 配额 | 负载 | 重复 | 窗口 s | Agent CPU s | 最大读 ms | 采样点数 | 单次 metrics ms | 分配 MiB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 200m | steady | 1 | 10.02 | 0.206 | 7.5 | 41 | 15.1 | 11.54 |
| 2 | 200m | churn | 1 | 10.76 | 1.982 | 1044.2 | 30 | 2104.5 | 101.35 |
| 3 | 1 | steady | 1 | 10.02 | 0.163 | 2.9 | 41 | 6.8 | 13.40 |
| 4 | 1 | churn | 1 | 10.07 | 1.858 | 23.3 | 41 | 164.9 | 128.69 |
| 5 | 1 | churn | 2 | 10.09 | 2.134 | 53.7 | 41 | 93.8 | 110.11 |
| 6 | 1 | steady | 2 | 10.03 | 0.140 | 2.3 | 41 | 7.9 | 11.58 |
| 7 | 200m | churn | 2 | 10.53 | 1.598 | 504.5 | 41 | 305.7 | 129.67 |
| 8 | 200m | steady | 2 | 10.04 | 0.177 | 3.1 | 41 | 9.2 | 13.36 |

churn 的分阶段墙钟均值与限流如下。各 stage 存在嵌套/并发，不能相加解释为 CPU 利用率。`throttled_usec` 可能跨多个 CPU 累计，不是该窗口的停顿百分比。

| 指标 | 200m / 1 | 200m / 2 | 1 CPU / 1 | 1 CPU / 2 |
| --- | --- | --- | --- | --- |
| production map 扫描均值 ms | 267.96 | 25.82 | 12.38 | 16.22 |
| accuracy map 扫描均值 ms | 187.90 | 39.28 | 12.76 | 16.73 |
| CPU 聚合均值 ms | 35.76 | 2.30 | 1.82 | 1.93 |
| CRI 查询均值 ms | 254.06 | 212.10 | 16.72 | 21.34 |
| topology enrichment 均值 ms | 183.57 | 50.92 | 7.35 | 8.16 |
| topology refresh 均值 ms | 437.65 | 263.03 | 24.08 | 31.40 |
| metrics CPU maps ms（一次） | 428.11 | 95.70 | 24.32 | 30.44 |
| metrics runqlat maps ms（一次） | 1400.25 | 207.02 | 136.42 | 58.45 |
| Agent cgroup 限流累计差 s | 26.071 | 12.329 | 0.522 | 0.409 |

churn 全节点 CPU map 快照条目范围为 4165–7756，并非所有样本都达到 8192 容量。不能简单归因于“map 满了”。200m 两次读取边界 skew 为 9.80%、4.90%，1 CPU 为 0%、0.47%；因此不把这些诊断窗口的名义精度偏差作为精度优化验收。

## Profile 的解释边界

四个 churn profile 中 `CPUProbe.Snapshot` 的累计采样占比约 40–47%，`MapIterator.Next` 是主要热点，BPF syscall、序列化/内存搬运和 GC 均可见。但 `measureAccuracy` 本身的累计占比也约 41–44%：这是并行运行额外 250ms 扫描的**诊断负载**，不是生产环境常态，更不能据此称生产采样占用 40% CPU。累计调用栈比例存在包含关系，不可相加。

CPU profile 只对 Agent 进程采样，不能覆盖其他任务上下文中的所有 BPF 程序执行；其时间窗口与诊断快照、accuracy 请求也非原子对齐，不能将采样 CPU 秒与 cgroup 差值强行要求相等。stage 在完成时记账，窗口差只统计窗口内完成的操作，包含边界效应。共享宿主机噪声、只有两次重复、profiling 自身成本，都限制了因果解释。

## 验证与恢复

- `go test ./...`、`go vet ./...`，以及 telemetry/experiment/cpu/topology/metrics 的 race 检查通过。
- 8 个 case 的 Agent 均同 UID、Ready、零重启；保存的 8 次 scrape 中 `events_lost_total=0`、`metrics_collection_errors=0`。这不表示所有 CPU/runtime/runqlat 记账都无遗漏。
- 矩阵结束后原 DaemonSet spec 恢复一致，没有实验 Pod 或锁残留。
- 新 `:cost` 镜像在**不启用 debug** 的原生产参数下通过完整 `make integration`：lifecycle、CPU hotspot（88.25%）、runqlat（P95 31us → 2047us，PSI 102561us → 5912631us）。开始和结束状态记录同 UID、零重启。输出见 [integration 日志](2026-10-20-cost-integration.txt)。
- 集成测试外层临时恢复命令遭遇 PowerShell→WSL 引号展开问题，空镜像更新被 API 拒绝；随后使用明确的原镜像名称恢复，核对 [恢复状态](2026-10-20-cost-integration-restored.json) 与实验前 DaemonSet spec 一致。这是临时执行包装的问题，不是执行器的恢复失败。
- 非 loopback `--debug-listen=0.0.0.0:18090` 在初始化 BPF 前退出，exit code 2。
- warmup 中主动 SIGINT 的 [中断记录](experiments/20261020-cost-interrupt/summary.json) 如预期标为 `failed/context canceled`，没有 `restoreError`，原 spec 恢复一致。
- profile 与 accuracy 两个 HTTP 请求进行中主动 SIGINT 的 [第二次中断记录](experiments/20261020-cost-profile-interrupt/summary.json) 同样正确失败并恢复；请求在约 14s 时取消，后台负载、转发、实验 Pod 与锁均清理。

## 下一步的范围

先做 metrics map occupancy 的异步/低频采样与读取优化，不改变 map 容量和 BPF 记账语义；使用本矩阵定位成本变化，再用关闭诊断的 AB/BA 和原 accuracy 对照确认业务收益及精度。若考虑 BatchLookup，必须覆盖不支持时的回退、并发更新、部分批次和 EOF 行为；当前阶段没有提前实现这些优化。
