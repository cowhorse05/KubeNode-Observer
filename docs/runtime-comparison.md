# M10e：runc / Kata 配对运行时对照

`make m10e-runtime-test` 在同一个单节点测试集群上，用同一份实际镜像和同一组 Pod 资源限制比较 runc 与 Kata。实验采用 AB/BA 顺序，不把 Pod/VM 启动耗时混进业务窗口，也不把某次本地差值包装成普适的“Kata 开销”。

## 运行

前置条件与 M8 相同：本地 `kno-dev` 已安装 `kno-kata-qemu` RuntimeClass、Agent 正常运行、Docker/Minikube/kubectl 可用。脚本只接受显式的本地 context；不要把开发目标指向 tdk。

```bash
make m10e-runtime-test

# 有限覆盖参数；pairs 必须是 2..10 的偶数
M10E_PAIRS=4 \
M10E_DURATION_SECONDS=20 \
M10E_WARMUP_SECONDS=5 \
make m10e-runtime-test
```

Make target 从当前源码构建并预加载 `observer-benchmark` 与 Agent 镜像，临时替换 Agent 后在退出路径恢复原 DaemonSet spec。每个 capture 都新建同名 Pod，等待 Ready 后从 Minikube node 经 Pod IP 调用同一个 HTTP 控制面（Kata Guest 不使用错误的 Host-network localhost port-forward），执行固定的 `steady` 工作负载：8 个锁定 OS thread 的 worker 重复 SHA-256 和 `sched_yield`，先预热、再记录吞吐与内部 operation 延迟。

## 实验设计

每个 pair 各含一个 runc 和一个 Kata capture：

- 奇数 pair：`runc → kata`；
- 偶数 pair：`kata → runc`；
- 两侧 container 使用相同的镜像 digest、1 CPU limit/request、内存配置、worker 数和工作量；RuntimeClass 的 Pod overhead 仍由 Kubernetes 如实保留；
- Pod Ready 之前的容器/VM 启动时间不进入测量；
- 每个 capture 后删除 Pod，并等待 Agent 拓扑中的旧 Pod UID 和 Kata sandbox 消失；
- Agent 必须保持同一 Pod UID、Ready、零重启，实验期间 `eventsLost` 不得增加。

工作负载测量完成后，脚本另外采集一次 `/api/v1/host/resources`。该窗口只验证 runc container 或 Kata sandbox 的 Host 归属，不参与吞吐/P99 差值，因此不会把 API map 扫描成本偷偷混入性能窗口。

## 摘要为什么不盲信文件名

`hack/summarize-m10e.py` 重新校验原始产物：

- condition 文件必须符合 AB/BA 位置，不能把 runc capture 改名成 Kata；
- Kubernetes 返回的 `runtimeClassName`、Pod UID、node、Ready/restart 状态必须一致；
- 所有 capture 的实际 `imageID` 必须完全相同；
- runc 必须有 Host 可见的 container 且没有 Kata sandbox；Kata 必须有 `guest-unavailable` container、Sandbox 和正的 hypervisor PID；
- Host 资源必须按同一个 Pod UID 归属为 `runc-container` 或 `kata-sandbox`；
- workload 的时长、warmup、worker、work iterations、operations、throughput 和延迟分位数必须自洽。

方向不是通过条件。即使本次 Kata 吞吐更高或 P99 更低，只要采集契约成立，实验仍会成功并如实输出。超过 10 ms 的内部 operation 会被计入 `latencyOverflowWindows`，不会通过重试被隐藏；若溢出达到全部 operation 的 1%，该窗口还会进入 `p99CensoredWindows`，提示 P99 已触及直方图上界。

## 输出与解释

默认产物目录为 `log/m10e-runtime-<UTC>/`。`summary.json` 给出每一对的：

- `kataVsRuncThroughputDeltaPercent = (Kata / runc - 1) × 100%`；
- `kataVsRuncP99DeltaPercent = (Kata / runc - 1) × 100%`；
- 配对差值中位数和固定 seed、10,000 次重采样的探索性 bootstrap 区间；
- 原始 Pod、topology、Host attribution、workload、镜像和环境证据。

默认 4 对、短窗口只用于工程演示和方法验证。它不能排除顺序相关的宿主机噪声，也不代表其他 Kata hypervisor、内核、业务或 CPU 拓扑。P99 是 SHA-256 + `sched_yield` 内部 operation 的墙钟延迟，不是网络请求或生产 SLO。

2026-11-13 的本地正式运行完成 4 对、8 个 capture。吞吐差值中位数为 `-5.10%`，探索性区间 `[-26.53%, +7.40%]`，方向不确定；Kata operation P99 的 4 个配对差值均为正，中位数 `+170.83%`，探索性区间 `[+107.41%, +269.57%]`。这只是在当前单节点短时微基准中的观察，不是普适的 Kata 因果开销。原始数据、失败 pilot 和证据边界见 [M10e 验收报告](../log/2026-11-13-m10e-runtime.md)。
