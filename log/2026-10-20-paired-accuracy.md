# 可重复配对实验与 CPU 统计精度对照

日期：2026-10-20（Asia/Shanghai）
Review 分支：`codex/paired-accuracy-experiments`

## 交付内容

- `kno-experiment`：单节点 Minikube 实验执行器；
- off/on 与 on/off 交替的配对实验，负载预热、逐对差值和固定 seed 的配对 bootstrap；
- eBPF CPU runtime 与容器 `cpu.stat usage_usec` 的同窗口精度对照；
- steady/churn 两类负载、250 ms/1 s/2 s 三种 map 读取间隔、两次反向顺序重复；
- 原始数据、源码快照、镜像 ID、环境、参数、失败记录和恢复状态均自动落盘。

方法和复现命令见 [实验指南](../docs/experiments.md)。

环境以原始 `nodes.json` 为准：WSL2 内核 `6.6.114.1-microsoft-standard-WSL2`、Kubernetes `1.35.1`、containerd `2.2.1`，节点报告 CPU capacity 为 12；workload 限制为 1 CPU。节点暴露的 CPU capacity 与 Minikube 外层容器的 CPU 配额不是同一概念，本次不沿用旧报告中“2 vCPU”的笼统表述。

实验结束后补查 `docker inspect kno-dev`：`NanoCpus=0`、`CpuQuota=0`、`CpuPeriod=0`、`CpusetCpus=""`、`Memory=4294967296`（4 GiB）。该配置没有显式的 Docker CPU 配额或绑核设置；不能根据启动脚本中的默认 `--cpus=2` 推断本次已有集群只使用两个 CPU。

## 验证记录

预实验：[summary](experiments/20261020-pilot/summary.json)。1 s 间隔、3 s 窗口下，steady 误差为 -0.77%，churn 为 -2.80%。该短窗口只用于验证数据链路。

主动中断：[summary](experiments/20261020-interrupt/summary.json)。在 off 负载阶段发送 SIGINT，记录为 `failed/context canceled`；执行器恢复 Agent 原配置并清理实验 Pod 与 ConfigMap 锁，恢复后的 rollout 成功。

完整矩阵：[summary](experiments/20261020-paired-accuracy/summary.json)。这轮同时验证 4 对配对流程及 12 个精度 case；配对阶段早期曾有 Go 检查并行运行，因此其配对数字仅保留作流程验证，最终开销结论使用后续独立配对复测。

## 独立配对结果

[原始 summary](experiments/20261020-paired-isolated/summary.json)。本轮在 Go 检查完成后单独执行，期间未运行构建或其他测试；保持默认预热 3 秒、测量 10 秒、Agent settle 5 秒，共 8 个完整配对。

| Pair | Order | Off ops/s | On ops/s | Throughput delta | Off P99 us | On P99 us |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| 1 | off/on | 111,516.50 | 136,266.67 | +22.19% | 26 | 19 |
| 2 | on/off | 144,632.11 | 152,780.00 | +5.63% | 19 | 17 |
| 3 | off/on | 151,855.42 | 132,887.59 | -12.49% | 17 | 21 |
| 4 | on/off | 145,348.94 | 156,274.89 | +7.52% | 18 | 17 |
| 5 | off/on | 112,812.93 | 161,981.42 | +43.58% | 27 | 17 |
| 6 | on/off | 176,547.72 | 113,146.07 | -35.91% | 14 | 37 |
| 7 | off/on | 163,988.17 | 136,318.37 | -16.87% | 17 | 21 |
| 8 | on/off | 153,625.81 | 127,684.00 | -16.89% | 16 | 22 |

配对差值中位数及探索性 95% percentile bootstrap 区间（seed 20261020，10,000 次完整配对重采样）：

```text
throughput: -3.43%, interval [-16.89%, +22.19%]
P99:        +8.99%, interval [-26.92%, +37.50%]
```

两个区间都跨过 0，且轮次波动大。本轮不能可靠区分几个百分点的 Observer 开销；也不能将正向吞吐差值解释为 Observer 加速。所有异常轮次均保留，未通过删除 Pair 5/6 等方式缩小区间。该结果说明需要在更受控的 CPU 环境和更长窗口下继续测量，不能仅比较旧版顺序实验与本轮的两个点估计。

## 精度矩阵结果

每个窗口 10 秒、8 workers、1 CPU limit；Agent CPU limit 为 200m，production CPU collector 保持原来的 1 秒间隔。表中 interval 为精度读取器的名义间隔。

| Workload | Interval | Repeat | Signed error | Reference skew | Missing keys |
| --- | --- | ---: | ---: | ---: | ---: |
| steady | 250 ms | 1 | -0.70% | 0.00% | 0 |
| steady | 1 s | 1 | -0.67% | 0.00% | 0 |
| steady | 2 s | 1 | -0.45% | 0.00% | 0 |
| churn | 250 ms | 1 | -2.14% | 4.08% | 6,703 |
| churn | 1 s | 1 | -2.27% | 2.14% | 16,804 |
| churn | 2 s | 1 | -2.84% | 1.64% | 16,495 |
| churn | 2 s | 2 | -3.08% | 0.00% | 16,668 |
| churn | 1 s | 2 | -2.91% | 0.00% | 15,226 |
| churn | 250 ms | 2 | -3.16% | 0.00% | 14,234 |
| steady | 2 s | 2 | -0.54% | 0.00% | 124 |
| steady | 1 s | 2 | -0.65% | 0.08% | 122 |
| steady | 250 ms | 2 | -0.66% | 1.57% | 0 |

本轮 steady 名义误差范围为 `-0.70%..-0.45%`，churn 为 `-3.16%..-2.14%`。两次重复没有表现出一致的 interval/误差单调关系，不能据此宣称缩短间隔一定更准确。

重要的测量限制：

- churn/250 ms/repeat 1 的参考读取区间宽度达到 4.08%，只考虑该区间时 signed error 可落在约 `[-4.09%, -0.10%]`，不能将中点的 -2.14% 全部归因于漏计；
- churn/250 ms 两次实际只有 34 和 37 个扫描点（理想约 41），单次扫描最大耗时分别约 510 ms、602 ms，超过名义间隔；CPU 限额、调度与遍历成本的贡献仍需后续分项测量；
- 反向重复中的 steady 也有旧 key 消失，但 CPU 误差仍低于 1%；这些 key 可能来自同一容器此前已经结束的 churn 子进程，说明 missing 不能直接当作丢失率；
- 完整矩阵结束时 Agent Ready、restart 0，恢复后的 DaemonSet spec 与实验前逐字段一致。

完整矩阵和独立配对的所有保存 scrape 中，`eventsLost` 与 `metrics_collection_errors` 均为 0。

## 解释边界

- 精度接口共用 production collector 的 runtime delta 规则，但独立读取 map；它不积分 `/api/v1/cpu` 的已发布窗口；
- 精度对照包含额外 map 扫描成本，与开销配对实验分开执行；
- `missingKeys` 是相邻快照的 key 消失数，不是精确 eviction 计数；
- `referenceSkewPercent` 只描述 cpu.stat/BPF 非原子读取的时间偏差；
- `eventsLost=0` 只涉及生命周期 ring buffer，不能代替 CPU 精度对照；
- 4 个配对用于工程验证，统计区间需视为探索性，不能据此作生产开销承诺；
- 本阶段未修改 BPF map 容量或 production collector 的采集间隔。

## Review 与回归

实现提交：`703a5d8`。独立配对的 `git-head.txt` 指向该提交；完整精度矩阵由提交前的相同测量实现执行，其源码快照和 hash 已随数据保存。随后仅增加了 interval 重复参数拒绝校验及对应测试，不改变本轮默认参数下的测量或统计规则。

验证结果：

```text
go test ./...                                                     PASS
go test -race ./internal/cpu ./internal/experiment ./examples/observer-benchmark  PASS
go vet ./...                                                      PASS
Minikube pilot                                                     PASS
SIGINT cancellation + original DaemonSet restore                    PASS
4-pair workflow + 12-case accuracy matrix                           PASS
8 isolated AB/BA pairs                                             PASS
all saved JSON parse                                               PASS
```

最后运行 `make integration`，结果：

```text
lifecycle exec: observed
CPU hotspot: ranked first at 99.95%
runqlat baseline P95: 31 us
runqlat contention P95: 2,047 us
PSI baseline: 96,255 us
PSI contention: 5,847,054 us
```

本次只完成实验方法与精度对照，留待人工 review 后再决定下一项性能优化。优先评审：参考读取边界、短命 key 的 delta 语义、配对重采样方法，以及高 churn 下名义采集周期无法达到的问题。
