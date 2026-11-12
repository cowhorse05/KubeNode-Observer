# M10d：Host × Guest contention 证据摘要

`kno diagnose contention` 把 M10c 四象限的 `summary.json` 转成可读的分层信号。它是离线实验解释器，不连接 Agent、不读取生产环境，也不把相关性包装成自动根因。

```bash
go run ./cmd/kno diagnose contention \
  --summary log/m10c-test-20261112T022351Z/summary.json

# 供脚本消费
go run ./cmd/kno diagnose contention \
  --summary log/m10c-test-20261112T022351Z/summary.json \
  --output json

# 也可从 stdin 读取，输入上限 4 MiB
cat summary.json | kno diagnose contention --summary -
```

## 为什么不使用固定阈值

M10c 的 P95 是 log2 直方图桶上界，运行环境和窗口又很短。给它硬编码一个“超过多少微秒就是根因”的阈值既不可移植，也会制造精度错觉。因此摘要器按同一轮配对比较目标场景与 `h0g1` 参考场景：

| 场景 | 配对 | 只在什么条件下给出主要信号 |
| --- | --- | --- |
| `guest-only` | `h0g1 → h0g8` | 每轮 Guest P95 都更高，Host vCPU 0 每轮不变 |
| `host-only` | `h0g1 → h1g1` | 每轮 Host vCPU 0 P95 都更高，Guest 每轮不变 |
| `mixed` | `h0g1 → h1g8` | 每轮两侧 P95 都更高 |

如果方向在轮次间矛盾，输出 `no_consistent_*_signal`；如果目标域上升但另一个域也变化，输出带 `*_with_*_change` 的保守结果，而不是强行归因。

## 输入不会被盲信

命令重新检查而不是复述 Python 摘要中的方向字段：

- `result` 必须是 `captures_valid`，scope 必须是 M10c 四象限；
- `capturePairs` 必须等于 `rounds × 4`；
- 四个象限的 Host 开关、Guest worker 数、样本数和数组长度必须互相一致；
- P95 必须是有限非负数；
- quality warning 的计数必须与窗口列表一致；
- 至少需要两轮，并且不能有 partial/incomplete coverage 或零等待样本，才能标为 `complete_for_descriptive_contrasts`。

质量不满足时仍展示原始逐轮比较，但三个 verdict 全部为 `insufficient_evidence`。结构自相矛盾则直接返回非零退出码。JSON 输出带版本 `kno.contention-diagnosis/v1`，方便后续展示层使用而不解析人类文本。

## 真实本地结果

对 [M10c 最终冻结结果](../log/m10c-test-20261112T022351Z/summary.json) 的回放为：

```text
Evidence assessment: complete_for_descriptive_contrasts (2 paired rounds)

guest-only [h0g1 -> h0g8]: guest_internal_signal
  Guest P95: higher_in_every_round; ... median 127 -> 1535 us
  Host vCPU 0 P95: unchanged_in_every_round; ... median 127 -> 127 us

host-only [h0g1 -> h1g1]: host_vcpu_signal
mixed [h0g1 -> h1g8]: mixed_signal
```

这只说明受控实验中的分层信号与构造条件一致。两轮 3 秒窗口不构成统计显著性或性能保证；`32767 us` 是桶上界；Host 与 Guest 时钟仍未建立可用于窗口交集的误差上界。
