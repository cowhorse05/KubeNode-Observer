# 配对开销实验与 CPU 统计精度对照

这组实验回答两个独立的问题：Observer 对业务的吞吐/尾延迟影响有多大；CPU runtime 的统计结果与同一容器 cgroup 的 `cpu.stat` 相差多少。

## 使用方法

在 WSL/Linux 项目根目录执行，要求单节点测试集群、已部署当前代码构建的 Agent。所有镜像均从本地构建并 load，不需要拉取项目运行时镜像：

```bash
make install-minikube
make experiment
```

分别执行：

```bash
make benchmark   # 4 个配对，off/on、on/off 交替
make accuracy    # steady/churn × 250ms/1s/2s × 2 次
```

可以覆盖实验参数，输出目录必须不存在：

```bash
make experiment EXPERIMENT_FLAGS='--pairs 8 --duration 20s --warmup 5s --output log/experiments/my-run'
```

`--pairs` 必须为大于等于 2 的偶数；默认每次测量 10 秒、负载预热 3 秒。`--intervals` 只影响精度实验的额外 map 读取器，不修改 Agent 默认的 CPU collector 间隔。默认 context 为 `kno-dev`，可用 Makefile 的 `MINIKUBE_PROFILE` 或 CLI 的 `--context` 显式指定其他单节点测试集群。

实验期间会删除/重建 `kno-agent` DaemonSet。每个 on 样本都从新 Agent 开始，等待 Ready 和 5 秒 settle；因此当前结论限定为相同预热后的 Agent 状态。结束、失败或收到 SIGINT/SIGTERM 时恢复实验开始前保存的 DaemonSet 配置，并删除实验 Pod。`kno-experiment-lock` ConfigMap 防止两个执行器同时修改 Agent；SIGKILL/断电不执行清理，需要按 `original-agent.json` 恢复并检查残留锁。

## 配对实验

- 同一个 1 CPU limit 的 workload Pod 保持运行，避免把不同 Pod 创建时间当作业务差异；
- workload 由 HTTP 控制启动固定的 8-worker SHA-256 + sched_yield 任务；
- 预热阶段持续执行相同工作，但不记入吞吐和延迟；
- 第 1、3…组按 off/on 执行，第 2、4…组按 on/off 执行；off 必须确认所有 Agent Pod 已删除；
- 保存每个 on 样本前后 Agent UID、Ready、restart 状态及 `/metrics`，重启或替换会使该实验失败；
- `pair delta = (on / off - 1) × 100%`，报告各配对差值的中位数；
- 95% bootstrap 区间以完整配对为重采样单位，固定 seed 为 20261020、重复 10,000 次。

默认 4 个配对仅用于工程验证，bootstrap 区间是探索性结果。区间跨过 0 时，不能据此宣称观测开销为 0；增加轮数后仍需检查异常值和宿主机负载。

P99 是单次内部 operation 的墙钟耗时，包含 SHA-256 和 sched_yield，属于闭环 CPU 微基准，不代表 HTTP 业务请求尾延迟。直方图精度为 1 us，超过 10 ms 的 operation 计入 `latencyOverflow`；若尾部命中上限，应增大直方图范围后再解释 P99。

## CPU 精度对照

`GET /api/v1/cpu/accuracy?cgroupId=<id>&duration=10s&interval=1s` 只接受当前拓扑中存在的容器 cgroup，并在每次读取时校验 cgroup inode，避免容器删除或路径复用后继续累计。

每个扫描点读取顺序：

```text
cpu.stat usage_usec（before） → BPF runtime map snapshot → cpu.stat usage_usec（after）
```

对同一 cgroup 内所有线程累计相邻 map 快照的 runtime delta，与 production CPU collector 共用 `runtimeDelta` 规则：已有 key 做差，新 key 或计数回退时取当前累计值。首次快照只建立基线。此接口独立读取 map，不积分 `/api/v1/cpu` 已发布的约 1 秒窗口，也不验证 Kubernetes 身份映射的完整正确性。

参考 CPU 时间的读取边界区间：

```text
lower = last.before - first.after
upper = last.after - first.before
reference = (lower + upper) / 2
signed error = (sum(BPF deltas) / reference - 1) × 100%
reference skew = (upper - lower) / reference × 100%
```

纳秒和微秒在比较前统一为纳秒。参考读取区间只描述非原子读取的时间偏差，不是统计结果真实误差的严格上界；调度记账时机、BPF 更新时机和并发 map 遍历也可能产生差异。

两类 workload：

- `steady`：8 个常驻 worker，持续计算与 yield；
- `churn`：8 个 worker 持续 exec 短命子进程，每个子进程完成固定 SHA-256 工作后退出，父进程等待回收。

执行器先确认 workload 正在运行，再预热，并在精度窗口结束时确认负载仍未结束。第 2 次重复会反转负载与 interval 的顺序，以减少固定顺序偏差。每次保存 throttle 时间、采样读耗时、目标及全节点 map 条目数，以及：

- `newKeys`：本次出现、上次没有的 key；
- `missingKeys`：上次存在、本次没有的 key，不能直接等同于精确 eviction 次数；
- `resetKeys`：本次累计值低于上次的 key。

整个生命周期发生在两次采样之间、并在下次读取前已被淘汰的 key，不会出现在 `missingKeys` 中；相反，早已完整采集的退出线程 key 被淘汰，也会被计入 missing。因此应以 cgroup 对照误差评价漏计，不能用 missing key 数直接计算丢失率。实际扫描间距可能受 CPU 限流和 map 读取耗时影响，应读取 `samples.at` 的差值，而不能只使用配置中的 interval。

生命周期 `eventsLost` 仅描述 ring buffer 预留失败，不代表 CPU runtime 或 runqlat 没有遗漏。精度实验会额外产生 map 读取成本，因此不用于计算配对开销实验的业务影响。实验成功表示流程和数据采集完成，不代表统计误差自动满足某个精度门限。

## 产物

默认目录为 `log/experiments/<UTC timestamp>/`，保留成功、失败和中断结果：

- `summary.json`：参数、配对结果、区间、每个精度 case 和执行状态；
- `pair-*-workload.json`：每次业务测量原始结果；
- `accuracy-*.json`：每个精度窗口和所有采样点；
- `*-metrics-*.txt`：Agent 自监控原始数据；
- `original-agent.json`、`restored-agent.json`：恢复前后配置和状态；
- `workload-pod.json`、`workload-status.json`：负载配置和实际 image ID；
- `nodes.json`、`kubernetes-version.json`、`go-version.txt`：环境；
- `git-head.txt`、`git-status.txt`、`source.patch`、`source.tar.gz`、`source-sha256.json`：代码版本和完整相关源码，包含未提交的新文件；
- `commands.jsonl`：命令、HTTP 请求的执行时间和错误信息。

复现的是相同实验方法与配置，不承诺共享 WSL2 环境每次得到相同的数值。
