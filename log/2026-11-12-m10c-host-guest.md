# M10c：Host × Guest 四象限本地验收

日期：2026-11-12

环境：本地 WSL2 + Minikube `kno-dev`，Kata QEMU Guest

冻结实现：`d5ee8dc449e77ef86864fc36eacc1bb95aeea5f4`

最终原始结果：[`m10c-test-20261112T021122Z`](m10c-test-20261112T021122Z/)

范围：本地开发验收；未连接、修改或部署 tdk

## 结论

M10c 已形成一个可复现、会严格失败的 Kata Host × Guest 观测实验，而不是“跑出预期趋势才算通过”的演示脚本。最终两轮共 8 个窗口全部满足身份、时长、读取括号、CPU/runqlat 非空、Guest 稳定线程覆盖和 Agent 健康门禁。

这次样本中，单独增加 Guest workers 时，Guest runqlat P95 从 `95 us` 上升到 `2047 us`，而 Host vCPU 0 P95 没有同步上升；单独开启同核 Host contender 时，Host vCPU 0 P95 从 `191 us` 上升到 `32767 us`。两种压力同时存在时，两侧 P95 都为 `32767 us`。这些结果说明两层探针能够呈现不同的调度压力信号，适合作为排障线索；它们不证明自动根因、普遍性能比例或跨时钟域事件重叠。

## 实验设计

- Host 侧只把 QEMU `vCPU 0` 线程固定到 Host CPU 0，再把 runc contender 固定到同一 CPU；不固定整个 VMM。
- Guest 侧用静态 pthread workload，一次创建 1 或 8 个 worker，全部固定在 Guest CPU 0，ready 前完成建线程，窗口内不增删线程。
- 四象限为 Host contender 关/开 × Guest workers 1/8；第一轮正序，第二轮反序。
- Host 与 Guest 资源请求并发发出并保存各自 `timing=true` 读取括号，但不相减两域时间戳，也不使用 M10a 条件 offset 推导重叠。
- 趋势字段只进入摘要；脚本退出码只由采集契约和健康门禁决定。

## 最终结果

下表为两轮原始 P95 数组及其中位数。`h` 表示 Host contender，`g` 表示 Guest workers。

| 场景 | Guest runqlat P95 us | Guest 中位数 | Host vCPU 0 runqlat P95 us | Host 中位数 | Guest 覆盖 |
| --- | --- | ---: | --- | ---: | --- |
| `h0g1` | `[127, 63]` | 95 | `[255, 127]` | 191 | 两轮均 `2/2` |
| `h0g8` | `[2047, 2047]` | 2047 | `[127, 127]` | 127 | 两轮均 `9/9` |
| `h1g1` | `[255, 127]` | 191 | `[32767, 32767]` | 32767 | 两轮均 `2/2` |
| `h1g8` | `[32767, 32767]` | 32767 | `[32767, 32767]` | 32767 | 两轮均 `9/9` |

补充质量证据：

- `capturePairs=8`，`partialOrIncompleteCoverageCount=0`，`zeroWaitSampleCount=0`；
- 每窗 CPU 与 runqlat 都非 null，实际窗口不短于请求的 3 秒；
- Pod UID、完整 Sandbox ID、VMM PID/generation 与唯一 vCPU 0 TID 在窗口间一致；
- 条件文件与 Guest workload 输出交叉确认 contender 状态、worker 数和 Guest CPU 0 affinity；
- Agent Pod UID、container ID 和 `restartCount=0` 在实验前后相同；
- `kno_agent_events_lost_total` 前后均为 0；
- 原 DaemonSet spec 恢复后与实验前逐字相同，测试 namespace 和实验锁均已删除；
- `source-head.txt` 为冻结 SHA，`source-status.txt` 与 `source.patch` 均为空，参与实验的关键源码另有 SHA-256 清单。

## 失败尝试没有被隐藏

最初两次运行复用了 Go contention workload，均按硬门禁退出 1：

1. [`m10c-test-20261111T064819Z`](m10c-test-20261111T064819Z/) 在多个窗口出现不完整覆盖；例如 `h0g8` 的线程集从 16 变为 17，CPU/runqlat 因稳定线程契约不成立而返回 null。
2. [`m10c-test-20261111T065349Z`](m10c-test-20261111T065349Z/) 增加固定预热后仍在 Host contention 下出现 `h1g8` 线程集 15→19。固定 sleep 不能保证 Go runtime 在受抢占环境中已稳定。

因此没有放宽 Guest Probe、删除 partial 门禁或“重试到好看”。最终改为专用 pthread workload，使线程生命周期成为 workload 自己能保证的实验条件。最终结果仍是单次预先规定的两轮矩阵，不是按趋势筛选的最佳运行。

## 能说明与不能说明的范围

可以说明：

- 当前身份关联链能把同一个 Kata Pod 的 Host VMM/vCPU 与 Guest process 分层展示；
- 受控同核压力下，Host vCPU 等待和 Guest 内部 runnable 等待能呈现不同组合；
- partial/null、覆盖率、Agent restart/loss 和清理恢复均受到可审计门禁约束。

不能说明：

- 两轮、3 秒窗口不足以形成统计显著性、容量结论或通用性能比例；
- `32767 us` 是 log2 直方图桶上界，不是精确分位数；
- Host contender 与 Guest 延迟同时变化仍只是受控实验中的关联，不是生产环境自动根因判定；
- 本实验没有比较 runc/Kata，也没有测 Probe off/on 开销；
- Host/Guest `timing` 仍属于两个时钟域，未建立可用于窗口交集的误差上界，因此不能声称跨层精确重叠。

后续若继续投入，优先增加独立重复、受控环境元数据、同负载 runc 参考和 Probe off/on 成本对照；在这些完成前，不把本地验收升级为 tdk 正式实验或生产保证。
