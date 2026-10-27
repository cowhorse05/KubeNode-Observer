# 2026-10-26 CPU 记账正确性与 runc 稳定性收尾

## 范围

从已合入 PR #3 的 main `30c3043` 开始。本阶段不安装 Kata、不修改 containerd/runtime 配置、不改默认 GOMEMLIMIT；保留 200m CPU /128 MiB memory 预算。实现为 `36fcfb1`（调度器 runtime + CPU-local baseline）、`b266aee`（独立 cgroup 总量 + batch 明细读取）。进程明细仍有 8192 条上限，新增 cgroup 总量上限 1024，CPU baseline 改为每 CPU 一个 slot。

## 根因拆分与真实内核实验

[初始保留实验](2026-10-26-cpu-retention.txt) 在无主动轮询时执行 128 个 `/bin/true` 子进程，使用独立 comm 识别，避免 WSL 用户 PID namespace 与 BPF initial namespace PID 不同导致误判：

- 8192 容量保留 128 条，原 tracepoint 墙钟差累计 / wait4 CPU 为 0.9350；
- 刻意缩为 16 后，只保留 4 条，比率 0.0201。它证明进程明细保留与容器总量是不同需求，**不证明原来的全部约 3% 偏差都是 LRU 淘汰**。

仅改为调度器 `se.sum_exec_runtime` 差值后，[相同目的的实验](2026-10-26-cpu-scheduler-counter.txt) 在 8192 容量下为 1.0007，16 容量仍严重丢失。但后续检查一次为 1.0288，10-27 开发环境恢复后的检查更达到 1.2830。这表明该超短任务 wait4 对照不适合作为精度校准；其退出末尾记账、计量口径与运行环境都有限制，不能只引用最接近 1 的一次。测试仅验证保留行为，正式 CPU 精度结论使用后面的同窗口 cgroup 对照，不将早期比率当成计时改动收益的独立统计证明。

新 baseline 使用 PerCPUArray，切入时存 next 的 PID 和调度器累计量，切出时匹配 prev 再做差。它不依赖一个可能被淘汰或 PID 复用污染的全局 PID 基线，也不把两次 tracepoint 的墙钟差当作精确调度器 CPU 时间。依据：[Linux 6.6 sched_switch 调用位置](https://github.com/torvalds/linux/blob/v6.6/kernel/sched/core.c)、[调度器 runtime 记账](https://github.com/torvalds/linux/blob/v6.6/kernel/sched/fair.c)。只验证了本项目普通容器负载，未验证所有调度类别/内核。

同一 slice delta 同时累加到进程明细和 cgroup 总量。Pod/Container 使用后者，Process 使用前者，API 的 `accounting` 明确来源；下钻之和不保证等于总量。cgroup map 仍有有限工作集与重置边界，不提供无限保留保证。

## 并发读取问题与失败证据

当天旧镜像 `kubenode-observer/kno-agent:memory-flush` 的 [baseline 重测](experiments/20261026-cpu-baseline/summary.json) 完成 3 个 steady 窗口后，在 churn 阶段报 `iterate CPU runtime map: iteration aborted`，已保留失败及恢复状态，没有补造一组成功结果。

进程明细现使用 batch lookup，按游标前进，保留最后一次 EOF 同时返回的条目；限制扫描工作量，零进度/不支持/系统调用失败均显式报错，不回退成不完整结果。[内核文档](https://docs.kernel.org/bpf/map_hash.html) 说明 next-key 在当前 key 被删除时可能从头开始，并建议并发删除时使用 batch。batch 仍非原子，不解决明细保留容量本身。

初版真实内核保留测试也在 tiny map 的并发写入下中止，见 [首次检查](2026-10-26-cpu-final-checks.txt)。测试现先 detach writer 再检查最终保留量，避免将测试读数过程的反复淘汰与负载结束时的保留行为混为一谈。没有删除这次失败。独立测试另外覆盖满容量 map 的实际 batch 读取。

## 候选完整配对与精度对照

原始数据：[20261026-cpu-accounting](experiments/20261026-cpu-accounting/summary.json)。单节点 WSL2 /6.6 内核 /Go 1.22.2 /Kubernetes 1.35.1，节点报告 12 CPU。被测 `:cpu-accounting` 的 image ID、参数、源码归档及 SHA256 均在各 Pod 状态和实验目录保存。没有与性能测量并行编译/执行其他实验。

4 组 off/on、on/off 配对，每窗口 10s：吞吐差值中位数 −3.89%，探索性 95% bootstrap [−15.71%,+11.73%]；P99 +10.71%，区间 [−5.88%,+25.00%]。闭环微基准、共享 WSL、小样本，仍然不能声称无开销或性能等价；也没有证明相比旧版业务吞吐改善。

| 负载（各 6 窗口） | cgroup 主结果 signed error 范围 | 中位数 | 最大 reference skew |
| --- | --- | --- | --- |
| steady | −0.5367%..+0.1351% | 约 0.0000% | 0.0526% |
| churn | 约 −0.0000%..+0.0104% | +0.0050% | 0.4159% |

这是同窗口 cgroup 累计与 `cpu.stat` 的工程对照，不是绝对零误差。部分参考区间宽于名义误差；snapshot 非原子，切出结算与内核实时计数窗口也不完全相同。原始 `processRuntimeNs` 保留了同窗口进程明细累计：本轮 churn 的明细也很接近参考，说明不能将主结果改善单独归功于 cgroup map；调度器计时、batch 读取也一起改变。容量不足时明细不可靠的证据来自前面的独立保留实验。

旧版 baseline 当天未完成，故没有凑成新旧版本的完整精度 AB/BA，不宣称统计等价性。本轮证明特定场景下偏差已明显收敛，尚非所有负载的精度承诺。production API 单测验证进程明细完全消失时 Pod/Container 仍保留 cgroup delta、实际采样时间戳、空总量不偷偷回退成明细加总；integration 另外检查三层 API 的 accounting 来源。

## 稳定性与恢复

预跑脚本第一次请求 limit=4096，超过事件 API 的 1000 上限而失败，保留于 [pilot](soak-pilot-20261026/exit.json)，已恢复部署/清理 namespace 和锁；修正后 [pilot-v2](soak-pilot-v2-20261026/exit.json) 完成 2 Pods /4000 子进程 /38s。长观察仅查询 32 条保留事件验证身份，不宣称覆盖全部进程事件。

CPU 初版镜像的 [第一次长观察](soak-20261026/exit.json) **失败**：63 轮 /126000 子进程 /1213s 的已保存检查通过，warm 样本约 24.19..28.50 MiB、启动峰值 84.07 MiB；第 64 轮 Pod 成功退出（约 4s 生命周期），之后 20 次按 namespace/Pod 查询均无保留事件。没有保存到这一轮的 unfiltered events/topology，无法唯一判定为空的全部原因；不能把此前健康样本扩充成整场无丢失/OOM 的证明。恢复的 DaemonSet spec 与原配置一致，namespace/锁清理，失败不计为 30 分钟通过。

检查发现原实现真实存在启动竞态：首次 unresolved exec 发起 CRI 刷新后，对同一 cgroup 限频 30s；若第一次读取过早，容器可在周期校正之前退出且没有被缓存。`2993b52` 改为全局合并的有界重试队列：首次立即尝试，后续刷新完成后至少等待 1s，单项最多 5 次，最多 128 项、30s 过期；成功解析立即移除。不要求再次 exec 才重试，也不再永久保留未知 cgroup 的限频键。单测覆盖首读失败后无第二次 exec 仍恢复、未知项重试上限/过期、并发去重/容量。它支持该竞态假设，但不将一次空查询无证据地全部归因于它；亚秒容器和历史事件环淘汰仍是边界。

新版 [独立长观察](soak-v2-20261026/exit.json) 已于 10-26 完成：93 Pods /186000 子进程 /1816s，共 95 个内存样本。Agent 同 UID、零重启、Ready，保存的 eventsLost 和 memory max/oom/kill 均为 0；每轮保留事件的 Pod UID/Container ID 均匹配。启动以来 peak 80.36 MiB，结束 current 26.98 MiB；300s 后样本为 23.98..27.62 MiB。300–600s 的均值25.57 MiB，1200s以后为26.08 MiB，不能据此宣称绝无增长或永不泄漏。最终 RSS 52.03 MiB、Go heap 5.56 MiB，与 cgroup charge 不是同一口径。

最终 cache 三 source ready、collection errors=0，但 runqlat lifetime refresh errors=2，未隐藏并发 census 失败。测试 namespace 的当前 topology 最终为空；这是当前容器清理，不代表 5 分钟 tombstone 应立即消失。原 DaemonSet spec 恢复一致、Ready，namespace/锁均清理。10-27 复核时六个被测核心/脚本文件 SHA256 均与这次记录一致，实际 Agent image ID 为 `sha256:0257f246e68c138ef43268dc8a0b925c3911a8d1dd423338965653055488bd6e`。发生失败时脚本另保存 unfiltered events、topology、Agent 状态与日志，不用重试覆盖失败记录。

## 10-27 最终镜像配对与精度回归

有界拓扑重试加入后的最终 `:cpu-accounting-v2` 镜像另跑完整实验，未复用初版的成功结果：[原始 summary](experiments/20261027-cpu-accounting-v2/summary.json)、[控制台](2026-10-27-cpu-accounting-v2-console.txt)。4 组 AB/BA、每窗口 10s，吞吐差值中位数 +0.25%，探索性 95% bootstrap [−4.85%,+6.74%]；P99 +6.70%，区间 [−11.76%,+21.43%]。区间跨 0，不能据此宣称性能提升、无开销或版本等价。

| 负载（各 6 窗口） | cgroup signed error 范围 | 中位数 | 最大 reference skew |
| --- | --- | --- | --- |
| steady | −0.1644%..约 0.0000% | 约 0.0000% | 0.3024% |
| churn | −0.0513%..+0.0008% | 约 0.0000% | 0.0804% |

采样间隔为 250ms、1s、2s，各重复两次、第二遍逆序。参考读取区间和非原子/切出结算边界仍适用，不能将小数读成普遍精度保证。summary 状态 complete、无 restoreError；原部署恢复状态另有存档。本次开发环境曾在前一晚退出，10-27 仅重新启动原 Minikube Docker 容器及其 containerd/kubelet，没有重建集群或拉取外网镜像。

## 验证与剩余边界

[CPU 初版 Go 检查](2026-10-26-cpu-verified-checks.txt)：全套 test、vet、cpu/probe/experiment/metrics/telemetry race、真实内核保留与 batch 测试、shell 语法均通过。随后扩充空/部分/容量压力 batch 测试时，[v2 检查](2026-10-26-cpu-v2-checks.txt) 的 Go/race 已通过，但尝试插入 8192 条后假设 LRU 必保留 8192 条的断言失败（实际 8064）。测试现改为与无 writer 的实际 map iterator 结果比较，不把插入次数当成保留量。[10-27 最终检查](2026-10-27-cpu-final-checks.txt) 全套 test、vet、六包 race（增加 topology）、真实内核保留、空/部分/容量压力 batch、shell 语法和 diff 检查均通过。失败记录单独保留，不纳入成功统计。

最终同一 `:cpu-accounting-v2` 镜像的 [集成与 40000 子进程回归](cpu-v2-validation-20261027/) 也已通过，见 [控制台](2026-10-27-cpu-v2-validation-console.txt)：lifecycle 身份、CPU 三层 accounting、CPU hotspot（108.76%，窗口值不要求正好 100%）、runqlat baseline P95 31us /contention 1023us，CPU PSI 85396us→5977755us。两轮各 20000 子进程之后，同 Agent UID、零重启、eventsLost=0。即时稳定性样本 RSS 52359168 bytes、Go heap 6807280 bytes；最终 metrics 三 source ready、collection errors=0，但 runqlat lifetime refresh errors=2，保留该并发 census 失败计数，未将恢复 ready 描述为从未失败。

两个最终执行器均恢复原 DaemonSet spec；额外核对 spec 相等，恢复镜像 `kubenode-observer/kno-agent:dev`、Pod Ready/零重启，实验锁、临时 namespace 和测试 Pod 均无残留。完整精度实验、长观察与最终 integration 的实际 Agent image ID 相同。只重新加载本地构建镜像，没有为测试加 registry 域名或增加资源预算。

保留的边界包括 1024 cgroup /8192 明细容量、初始未建立 baseline 的首个切片、非原子读取、只在切出时结算而非持续实时更新的窗口差异、极短测量窗口/动态 cgroup 迁移/PID 身份复用，以及其他内核和调度类别。没有为了取得更好数值调高部署资源或隐藏失败。**runc 前置阶段的有限验收已完成，停在 M7 Kata 入口**；本阶段没有执行 Kata/KVM 安装、containerd/runtime 或 RuntimeClass 变更。下一阶段先验证 KVM/nested virtualization 的可用性，再决定 Kata 后端与 Host/VMM 识别范围。
