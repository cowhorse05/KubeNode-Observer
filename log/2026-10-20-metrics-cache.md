# 2026-10-20 Metrics map 采样优化与回归

后续内存预算诊断与 BTF 缓存释放见 [2026-10-23 报告](2026-10-23-memory-budget.md)。本报告的镜像与数据仍保持优化当时的版本，不将后续结果回填成同一实验。

## 交付范围

实现提交 `de31ec4`，验收脚本及 Windows/WSL 可移植性检查为 `f97de6a`、`a070a28`。本阶段不修改 BPF 程序、map 容量、CPU runtime delta 或 CPU 生产采集周期，也没有引入新依赖。完整实验代码、镜像 ID、参数、每次响应、源码归档和恢复状态随数据保留。

两项联合优化：

1. map occupancy 只调用 `NextKey`，不再逐项 `Lookup` / 读取 value。沿用非原子 census 的语义，在并发删除/淘汰时可能重复或遗漏，不能当成精确 eviction 次数。最多调用 `MaxEntries+1` 次，遇到遍历上限/系统调用错误即丢弃不完整计数并报错。
2. `/metrics` 只读不可变快照。单个后台 worker 首次立即刷新，之后在每次完成后等待默认 15s，不因慢刷新积压追赶任务，不因并发 HTTP 请求重复扫描。每个 source 独立发布；失败时暂留最近成功值并暴露错误，超过 3 倍间隔（默认 45s）省略整组 map 值，不伪装为零。退出时先取消并等待 worker，再关闭 map。

缓存的年龄、就绪状态、最近成功时间、最近刷新耗时/错误与累计错误都可见；标签固定为 exec/cpu/runqlat。CPU、RSS、heap 和生命周期健康计数仍按每次请求读取。该优化明确用最多约一个刷新周期的新鲜度换取低抓取成本，不把缓存值称为实时占用。

## 双版本配对实验

原始结果：[metrics-compare/summary.json](experiments/20261020-metrics-compare/summary.json)。

- baseline：优化前功能代码 `87a126e`，镜像 `kubenode-observer/kno-agent:cost`，实际 ID `sha256:e73f5b8b57113f1a5b4e349088887eb4d3329db8bfbfc7c81dd12635e88debe6`；
- candidate：新 `:metrics-cache` 镜像，实际 ID `sha256:f73cb53f23b58fe5e0517d6a0d3ed6e1db16400afcd2fdad76d58118a97fceca`；
- 单节点 WSL2/Minikube `kno-dev`，节点报告 12 CPU；两版本均 200m limit / 100m request，memory 保持 128 MiB；
- 同一个 1 CPU workload Pod，8-worker 闭环 churn；每个 case 新 Agent，Ready 后 settle 5s，确认负载运行并预热 3s，测量 30s；
- 每秒最多发起一次 GET，单请求串行，慢请求不积压追赶；4 个完整配对，交替 baseline/candidate、candidate/baseline；
- 不请求 profile、accuracy 或 runqlat。测量期间不编译、不跑其他测试；前后 diagnostics 只用于 Agent cgroup CPU 差和阶段计时；
- CPU rate = Agent cgroup CPU 秒 / 实际窗口秒，100% 表示一个逻辑 CPU；不是整个节点 CPU，也不包括所有其他任务上下文中的 BPF 成本。

```bash
./bin/kno-experiment --mode metrics --context kno-dev --duration=30s --pairs=4 \
  --output log/experiments/20261020-metrics-compare
```

| 配对 | 版本 | Agent CPU rate | 客户端 P50 ms | P95 ms | 最大 ms | 抓取次数 | 出现 collection error 的次数 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | baseline | 19.86% | 1196.56 | 2066.65 | 2631.40 | 23 | 5 |
| 1 | candidate | 10.33% | 9.45 | 26.82 | 81.29 | 30 | 0 |
| 2 | candidate | 9.52% | 10.73 | 33.53 | 41.62 | 30 | 0 |
| 2 | baseline | 15.97% | 525.52 | 1092.17 | 1105.84 | 30 | 0 |
| 3 | baseline | 19.26% | 1196.94 | 2172.88 | 2611.67 | 22 | 3 |
| 3 | candidate | 9.48% | 9.49 | 60.80 | 74.40 | 30 | 0 |
| 4 | candidate | 9.56% | 10.29 | 218.00 | 419.21 | 30 | 0 |
| 4 | baseline | 16.32% | 454.75 | 969.30 | 983.39 | 30 | 2 |

配对百分比变化的中位数：

- Agent CPU rate：**−44.69%**，探索性 95% paired bootstrap 区间 `[−50.77%, −40.40%]`；
- 抓取 P95：**−97.07%**，区间 `[−98.70%, −77.51%]`。

候选每个窗口都实际完成了两次后台 CPU/runqlat map 刷新，已计入 CPU 窗口，不是只测缓存命中。120 次候选响应的三组 source 均 ready，最大年龄不超过约 15s，保存的累计刷新错误为 0；旧版 105 次响应中有 10 次 collection error。两版本保存的生命周期 `eventsLost` 都为 0。旧处理器没有保存具体错误文本，不能事后断定每次失败的准确系统调用原因。

必须保留的限制：

- 这是 **1s 抓取的压力场景**；没有测 15s 抓取收益，不能推广为正常业务或所有部署的 CPU 降幅。
- key-only 与异步低频刷新一起改变，未做独立消融，不能分别给两项优化分配收益比例。
- churn 是同配置的闭环工作负载，不保证两版本实际进程到达率相等；CPU rate 是该实验条件下的总体观测，不是固定事件数量下的单位处理成本。
- 后台只有每窗口两次刷新，不能用 120 个缓存响应推导“120 次扫描均无错误”。并发修改下遍历仍可能失败，缓存会显式暴露。
- 客户端耗时包含 port-forward、HTTP、原始文件落盘和命令记录；不是纯服务端时间。每窗口只有约 22–30 个样本，P95 是 nearest-rank，不是稳定 SLO。
- 第 4 组候选最大耗时仍达 419ms，没有宣称尾延迟已消失；只有 4 对，共享环境噪声和小样本仍限制外推。

## 普通 off/on 与精度回归

候选完整回归：[metrics-regression](experiments/20261020-metrics-regression/summary.json)。

```bash
./bin/kno-experiment --mode all --context kno-dev \
  --test-agent-image=kubenode-observer/kno-agent:metrics-cache \
  --duration=10s --pairs=4 --accuracy-repeats=2 \
  --output log/experiments/20261020-metrics-regression
```

4 个 off/on 配对的吞吐变化中位数为 −4.34%，探索性区间 `[−22.66%, +1.70%]`；P99 变化中位数 +12.32%，区间 `[+5.88%, +42.86%]`。这仍是噪声很大的微基准，不能将 metrics 压力场景的 CPU 收益等同于总体业务收益，也不能宣称 Observer 开销变成零。

候选精度的部分 churn 窗口比历史记录偏差大，因此补跑了旧镜像的同环境 12 个窗口，保留于 [baseline-accuracy](experiments/20261020-metrics-baseline-accuracy/summary.json)，不是直接宣布“精度不变”。

```bash
./bin/kno-experiment --mode accuracy --context kno-dev \
  --test-agent-image=kubenode-observer/kno-agent:cost \
  --duration=10s --accuracy-repeats=2 \
  --output log/experiments/20261020-metrics-baseline-accuracy
```

| 版本/负载 | 名义 signed error 范围 | 中位数 | 最大 reference skew |
| --- | --- | --- | --- |
| baseline steady | −0.86%..−0.55% | −0.74% | 0.17% |
| candidate steady | −0.82%..−0.62% | −0.70% | 0.30% |
| baseline churn | −3.64%..−2.24% | −2.96% | 0.56% |
| candidate churn | −3.85%..−2.19% | −3.33% | 0.18% |

两版 churn 范围重叠，但候选中位数名义偏差仍多约 0.37 个百分点。版本是先后执行的两个 block、churn 是闭环吞吐而非固定到达速率，没有精度等价性的配对证据；不能声称完全没有退化，也不能将差值归因为本次改动。CPU/BPF 记账源码没有改变不代表动态采样误差必然不变。本阶段交付可复核的 24 个窗口，**没有把 CPU 精度问题标为已解决**。`missingKeys` 也不是精确丢失率。

## 功能与稳定性验证

2026-10-20 已完成 [完整 integration 与两轮 churn](metrics-validation-20261020/integration.txt)，[40000 次子进程](metrics-validation-20261020/stability.txt) 后 Agent 同 UID、零重启、生命周期丢失为 0。CPU hotspot 为 99.80%，runqlat baseline/contention P95 为 31us/2047us，PSI 为 77291us/5738534us。普通 API 上 `/diagnostics` 与 `/debug/pprof/` 均为 404。原 DaemonSet spec 恢复一致。

最终 cache 三组均 ready、最近刷新无错误，但 runqlat 的累计刷新错误为 **1**，不能描述为整个测试完全无刷新失败；保留了最近成功值并随后恢复，验证了错误可见性。RSS 达 135069696 bytes（约 128.81 MiB），Go heap 约 77.57 MiB。**两轮零重启不足以证明 128 MiB 长期内存平台**，需要继续核对 `memory.current/stat/events`，不能将 RSS、Go heap 与 BPF memlock 简单相加。

2026-10-23 收口时重新执行 Go 全套单测、vet、metrics/experiment/probe/telemetry 的 race 和真实内核 LRUHash/Array map 测试，均通过，输出见 [Go 检查](2026-10-23-metrics-go-checks.txt)。并发抓取不触发/等待扫描、独立发布、失败留旧并标错、过期省略、恢复、取消，以及有界遍历/错误传播均有单测覆盖。

## Review 入口

代码优先看 `de31ec4`，验收脚本看 `f97de6a`、`a070a28`；原始数据作为生成产物折叠于 PR diff，报告与实现不折叠。`ref-project/` 未纳入变更。完整复现说明见 [experiments.md](../docs/experiments.md)。
