# M10c：Host × Guest 四象限实验

本阶段优先回答一个可演示的工程问题：当 Kata workload 变慢时，Host vCPU 被抢占和 Guest 内部 runnable 竞争能否在各自观测域中呈现不同信号。它不是自动根因分析，也不依赖尚无真实上界的跨 VM 时钟模型。

## 四个场景

| 场景 | Host 同核竞争 | Guest workers | 主要观察 |
| --- | --- | ---: | --- |
| `h0g1` | 关 | 1 | 低竞争参考 |
| `h0g8` | 关 | 8 | Guest 内部 runqlat 变化 |
| `h1g1` | 开 | 1 | Host vCPU runqlat 变化 |
| `h1g8` | 开 | 8 | 两种压力同时存在 |

Host 侧从资源接口确认 QEMU `vCPU 0` 的唯一线程，只把该线程固定到一个允许 CPU；Host contender 是 runc Pod，并固定到同一 Host CPU。其余 vCPU/VMM 线程不被挤到这个单核，避免在 baseline 中人为制造整台 VM 的线程互抢。Guest 侧通过现有 workload 容器执行 `/guest-burn`，并硬校验 workers 实际固定在 Guest CPU 0，从而与 Host `vCPU 0` 对应。每个场景同时请求带 `timing=true` 的 Host 和 Guest 资源窗口，但不直接相减两域时间戳。

M10c 使用独立的静态 pthread burn，而不是复用 Go contention 进程。它一次性创建全部 worker、固定到 Guest CPU 0，预热 5 秒后才打印 ready，直到窗口结束才回收线程。这样 workload 构造与 Guest Probe 的“完整稳定线程集”契约一致；不是看到指标为空后重试，也不放宽 Probe。若测量窗口内线程集仍变化，Guest Probe 继续按原契约返回 partial/null，实验硬失败。

默认运行两轮。第一轮顺序为 `h0g1 → h0g8 → h1g1 → h1g8`，第二轮反向，减少单一固定顺序的影响，但两轮仍不足以形成统计或性能承诺。

## 运行

前置条件与 M9/M10b 相同：Linux x86_64、运行中的 `kno-dev` Minikube、已安装 `kno-kata-qemu-guest`、可静态链接 pthread 的 C 编译器、本地镜像构建能力及免交互节点访问。脚本只操作自有 namespace 和临时 Agent 镜像，退出恢复原 DaemonSet；不连接 tdk。

```bash
python3 -m unittest hack/m10c_test.py
make m10c-test

# 可显式缩短或增加重复；正式保留结果前不要按趋势反复重跑。
M10C_ROUNDS=2 M10C_WINDOW_SECONDS=3 make m10c-test
```

原始结果进入新建的 `log/m10c-test-<UTC>/`。`config.json` 固化请求轮数、窗口和镜像名，`setup.json` 固化 Sandbox、VMM/vCPU PID 和所选 Host CPU。每窗的 `*-condition.json` 记录 Host contender Pod UID/PID/phase 与 Guest worker 数，`*-workload.txt` 证明 workers 实际位于 Guest CPU 0；摘要器会交叉检查这些条件而不只相信文件名。`summary.json` 包含四象限原始值/中位数、两项描述性方向和质量警告：

- `guestLoadRaisedGuestP95InBothHostStates`：两个 Host 状态下，8 workers 的 Guest P95 是否都高于 1 worker；
- `hostContentionRaisedHostVcpu0P95InBothGuestStates`：两个 Guest 负载下，Host contender 开启时的 `vCPU 0` P95 是否都更高；
- `partialOrIncompleteCoverageWindows`：Host/Guest partial 或 Guest 线程覆盖不完整的窗口。
- `zeroWaitSampleWindows`：至少一侧 runqlat `count=0` 的窗口；这是合法零值，但该侧 P95 没有等待样本支撑。

方向字段无论 true/false 都不改变脚本退出码。硬失败只表示采集契约有问题，例如身份不一致、实际测量短于请求窗口、缺少 timing、CPU/runqlat 为 null、覆盖率结构非法、窗口缺失，或实验期间 Agent 状态/事件丢失计数发生变化。历史上已有的 restart/lost 计数不会被误归因到本次实验。runqlat `count=0` 是合法的观测零值，但会明确告警，不会伪装成有样本的 P95。

## 如何展示而不过度解读

演示时并排展示四象限摘要和一份原始 Host/Guest JSON，说明两个内核分别聚合自己的调度事件，Node 再用 Pod UID、Sandbox ID 和 VMM generation 关联身份。若预期方向未出现，应报告环境噪声、覆盖率和样本量，而不是调参或重跑到想要的结果。

该实验目前只覆盖 Kata 四象限。后续若值得投入，再增加同负载的 runc 参考和 Probe off/on 成本对照；不要把脚本存在或一次通过本身写成性能结论。

2026-11-12 的本地 Minikube/Kata 验收、两次失败尝试和最终原始数据见 [M10c 本地验收报告](../log/2026-11-12-m10c-host-guest.md)。这不是 tdk 正式实验。
