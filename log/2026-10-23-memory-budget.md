# 2026-10-23 Agent 内存预算与 BTF 缓存释放

## 结论与实现范围

此前完整 churn 回归出现 RSS 128.81 MiB，但 RSS 不等于容器的 cgroup charge，不能据此直接判定 128 MiB limit 已被突破。本次新增 opt-in 内存诊断与可恢复实验，先拆分口径，再通过 heap profile 定位并释放静态探针加载后不再使用的内核 BTF 类型缓存。

实现：`3bd6166` 增加 memory 实验与诊断；`842e391` 在三组 probe 完成加载/attach 后调用 `btf.FlushKernelSpec()`。复用依赖库 API，不强制 GC，不调整部署默认 GOMEMLIMIT，不缩小 BPF map，也不改 CPU/runqlat 算法或采样周期。

## 环境与可重复方法

2026-10-23，WSL2 Ubuntu / Go 1.22.2 / Linux 6.6.114.1-microsoft-standard-WSL2 / Kubernetes 1.35.1，单节点 Minikube `kno-dev`，节点报告 12 CPU、Docker 节点 4 GiB。已有集群停机后恢复原容器及 containerd/kubelet，没有重建集群或拉取 Docker Hub 镜像。

每个 case 都是新 Agent：128 MiB memory / 200m CPU limit，32 MiB / 100m request；同一 workload Pod 的有限闭环 churn，5 × 60s pulse，再观察 60s idle，每 5s 保存 diagnostics 和 metrics。约 360s、78 个点/case；pulse 间有控制请求间隙，不是恒定到达率。测量期间不同时运行宿主编译、单测或其他集群实验。heap profile 使用 `gc=0`，解析在全部观测结束后进行。

- [pilot](experiments/20261023-memory-pilot/summary.json)：两配置各 10s churn + 10s idle，仅验证实验链路。
- [原配置 / 80MiB 对照](experiments/20261023-memory-budget/summary.json)：同一旧镜像 `kubenode-observer/kno-agent:memory`，源码 `3bd6166`，实际 image ID `sha256:b1b49f5a31ef0e92369a2afb0e1275693f0a1b2e4ccf57f45e15194a61a13084`。
- [BTF 释放版本](experiments/20261023-memory-flush/summary.json)：`kubenode-observer/kno-agent:memory-flush`，源码 `842e391`，实际 image ID `sha256:b89cc63e79b7387b7822d71a34ae814b2d8d02282bdeb6ee84fbcf046c59cc6a`；保持原 GOMEMLIMIT，不叠加 soft limit。

三次运行均保存参数、源码归档/哈希、实际 Pod/image ID、完整原始响应和恢复状态。原配置没有 GOMEMLIMIT，runtime 查询值为 MaxInt64；80MiB 组查询值为 83886080。结束后 original DaemonSet 的 `.spec` 与 restored List 中 DaemonSet 的 `.spec` 比较一致。

复现入口：`make memory`；对具体已预加载镜像运行：

```bash
./bin/kno-experiment --mode memory --context kno-dev --duration=1m \
  --memory-agent-image=kubenode-observer/kno-agent:memory-flush \
  --memory-go-limits=original --memory-pulses=5 --memory-idle=1m \
  --memory-interval=5s --output log/experiments/<新目录>
```

## 有限窗口观测结果

内存单位 MiB；末点是 idle 结束时，不是稳态数学保证。CPU rate 使用 Agent cgroup `usage_usec` 增量除以实际首末采样墙钟时间，100% 代表一个逻辑 CPU。

| 配置 | 末点 cgroup current | cgroup 生命周期 peak | 末点 RSS | 末点 HeapAlloc | 末点 Go managed | 窗口 GC 次数 | CPU rate |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 保留 BTF / 原配置 | 105.93 | 106.94 | 130.66 | 82.10 | 102.03 | 28 | 6.80% |
| 保留 BTF / 80MiB | 80.06 | 83.61 | 104.78 | 57.05 | 75.94 | 62 | 7.53% |
| 释放 BTF / 原配置 | 26.22 | 81.74 | 51.05 | 5.58 | 21.52 | 294 | 7.48% |

三组 `memory.events` 的 max/oom/oom_kill 均为 0，生命周期 eventsLost 均为 0，Agent 同 UID、零重启。后两组各记录一次 map census refresh error，最终 cache 已恢复，三 source ready；不能称为整个过程零采集错误。

BTF 释放组前三轮 pulse 末点 cgroup current 为约 25.0、28.0、26.0 MiB，最终 26.22 MiB；但初始采样仍为 72.63 MiB，启动以来 peak 81.74 MiB，说明启动预算必须保留，不能把 limit 下调到 30 MiB。

释放缓存与 soft limit 均伴随更多 GC，观测 CPU rate 也略高于原配置。三组是顺序单次观测，不是 AB/BA 配对，且闭环负载速率会变化，不能把 CPU 差值全部归因于 GC，或据此宣称 CPU 优化。此处没有新的业务吞吐/P99 或 CPU 精度等价性结论。

## 根因证据与修复边界

[旧版 after-idle heap profile](experiments/20261023-memory-budget/memory-01-original-after-idle-heap-top.txt) 的采样 in-use 总量约 39.34 MiB，其中 BTF `indexTypes`、`readAndInflateTypes`、`func2`、`readStringTable` 的互不重叠 flat 合计约 34.08 MiB。[新版 profile](experiments/20261023-memory-flush/memory-01-original-after-idle-heap-top.txt) 的总采样 in-use 约 5.87 MiB，前 25 个节点（覆盖 100% 采样分配）已无 BTF 保留路径。profile 是上次 GC 的采样估计，不等于即时 HeapAlloc 或 RSS，不将 cum 重复相加。

依赖的 [cilium/ebpf v0.17.3 实现](https://raw.githubusercontent.com/cilium/ebpf/v0.17.3/btf/kernel.go) 明确提供缓存清理：移除全局 kernel/module spec 引用，之后 LoadKernelSpec 可按需重新加载。当前 probe 全部静态加载完毕后才清理，运行中的 program/map FD 生命周期不受影响。若增加动态加载，需重新评估重复解析成本。

[Go 1.22.2 SetMemoryLimit](https://raw.githubusercontent.com/golang/go/go1.22.2/src/runtime/debug/garbage.go) 的口径为 runtime 管理的内存，近似 `Sys-HeapReleased`，不是进程或 cgroup 硬限制；因此 80MiB 只作为对照，不直接成为部署默认。[Linux cgroup v2 文档](https://docs.kernel.org/6.6/admin-guide/cgroup-v2.html) 定义 current/peak/max/events；RSS 还包含不同归属的共享页，不能用 RSS 与 cgroup limit 直接比较，也不能把 kernel/slab/map memlock 重复加入 current。

## 回归与尚未证明的内容

普通模式（debug 关闭）的 [完整回归](memory-flush-validation-20261023/integration.txt) 与 [40000 次短进程 churn](memory-flush-validation-20261023/stability.txt) 均通过。CPU hog 正确位居热点首位（单窗口 71.38%，不当成精度证明），runqlat P95 从 baseline 31us 增至 contention 1023us，PSI 同向上升；Agent 同 UID、零重启、eventsLost=0。churn 结束时 RSS 约 50.36 MiB、Go heap 约 8.60 MiB，后续抓取三 source ready、collection errors=0，但 runqlat lifetime refresh error counter=1。普通端口 diagnostics/pprof 均 404，恢复后的 DaemonSet spec 与运行前一致。

[SIGINT 恢复验证](experiments/20261023-memory-interrupt/summary.json) 在第一个 churn pulse 中用 `timeout --signal=INT --kill-after=90s 40s` 主动中断，保留 before/churn 共 3 个采样。summary 按预期标记 failed/context canceled，无 restoreError；原 DaemonSet spec 恢复一致，实验 Pod 和锁均已删除。此失败数据是恢复能力证据，不纳入内存结果表。

最终镜像额外完成 [12 个 CPU 精度窗口](experiments/20261023-memory-flush-accuracy/summary.json)：steady 名义 signed error −0.76%..−0.64%，中位数 −0.72%，最大 reference skew 0.17%；churn −3.48%..−2.45%，中位数 −3.22%，最大 reference skew 0.93%。steady missing keys 为 0；churn 仍有较多消失 key，不能将该计数解释为精确丢失率。此数据是另一天的顺序 block，不与 10-20 数据组成版本配对，也没有预设等价性界限；只能说明已复测且误差仍存在，不能宣称精度不变或已修复。结束恢复原 DaemonSet spec 一致。

最终 `go test ./...`、`go vet ./...`、metrics/experiment/probe/telemetry/cpu 五包 race、root 权限下真实 LRUHash/Array map 的 `TestMapStatsKernel`、`git diff --check` 均通过，见 [完整输出](2026-10-23-memory-final-go-checks.txt)。新增单测覆盖内存字段缺失/非法值、可选 peak、无限 max、路径穿越、RSS 格式错误及内存读取失败不破坏 CPU 诊断。最终集群原 Agent Ready、零重启，无实验 Pod/锁残留；`ref-project/` 未纳入修改。

这里只覆盖单节点、固定 workload Pod、约六分钟窗口。未覆盖小时级持续负载、Pod/元数据 churn、多节点或其他内核/BTF 大小；不证明不存在泄漏、所有机器都能遵守 128 MiB，或进程创建下 CPU 精度问题已解决。下一步应使用无诊断模式延长内存观察，并在更受控环境重复业务配对与精度对照，然后再推进 Kata Host/VMM。
