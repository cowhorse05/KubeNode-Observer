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

## 采集成本诊断

```bash
make cost EXPERIMENT_FLAGS='--duration=10s --accuracy-repeats=2'
# 已构建并加载镜像时，无需再次构建：
./bin/kno-experiment --mode cost --context kno-dev \
  --cost-agent-image kubenode-observer/kno-agent:cost \
  --duration=10s --accuracy-repeats=2
```

`cost` 不包含在 `all` 中，避免改变既有配对实验语义。当前固定矩阵为 steady/churn × Agent 200m/1 CPU，每个 case 都重建 Agent，偶数重复反转两因子的顺序，其他配置从原 DaemonSet 复制（CPU request 固定 100m，memory 保持原值）。独立镜像必须预加载，`imagePullPolicy=Never`；不会覆盖原 Agent 镜像。每个 case 内并行运行 250ms 精度扫描与 CPU profile，并在窗口中部抓取一次 `/metrics`，结束时校验 Agent UID、Ready、零重启以及负载仍在运行。所有 case 完成后才解析 profile，不在测量期间运行编译或测试。

Agent 的 `--debug-listen=127.0.0.1:18090` 默认关闭，仅接受字面量 loopback IP。通过 `kubectl port-forward` 访问 `/diagnostics` 和 `/debug/pprof/`，不接入普通 API mux。诊断无鉴权，只用于受控测试环境；hostNetwork Pod 的节点本机进程也能访问。不要公开转发诊断端口。

`/diagnostics` 提供进程启动以来固定标签的累计计时：production CPU map 扫描、accuracy 额外扫描、CPU 聚合、CRI 查询、拓扑 enrichment、含锁等待的拓扑刷新，以及 exec/CPU/runqlat 三组 map stats 与整个 metrics scrape。计数和总耗时在前后快照间做差，得到窗口次数、平均耗时与超过 100ms/250ms/1s 的次数。累计最大值只保留于原始快照，不错误地做差或当作窗口最大值。计时不在逐事件热路径内；debug 关闭时不记录。各 stage 可嵌套或并发，**不能相加视作 CPU 利用率**。

Agent cgroup 从 `/proc/self/cgroup` 定位，再读挂载的主机 cgroup v2 `cpu.stat` / `cpu.max`，不假设 `/sys/fs/cgroup` 根目录就是容器。前后差记录 `usage_usec`、`nr_throttled`、`throttled_usec`，以及 Go `TotalAlloc` 差值。限流时长是内核 cgroup 口径，可能包含多 CPU 累计，不能直接作为墙钟停顿百分比。CPU profile 用于定位 Agent 进程的采样热点，不覆盖其他任务上下文中的全部 eBPF 执行成本；`TotalAlloc` 是累计分配量，不是 RSS 增长。

Stage 在操作完成时记账，快照差统计的是窗口内完成的操作；跨边界的操作不会按时间切片。诊断快照、profile 和精度窗口也不是原子对齐的，不应把它们当作逐毫秒的因果追踪。

原始数据包含 `cost-*-agent-manifest.json`、`*-diagnostics-before/after.json`、`*-accuracy.json`、`*-cpu.pb.gz`、`*-cpu-top.txt`、`*-metrics.txt` 与负载/Pod 状态。`summary.costCases[].stageDeltas` 的 `maxNs` / `lastNs` 为 0（未定义窗口最大/末次），窗口最大读耗时来自独立精度采样的 `accuracyReadMaxSeconds`，其中包含前后 `cpu.stat` 读取。profile 和额外扫描会改变被测系统，该矩阵只用于诊断；要评价优化后的业务收益，仍须使用不启用诊断的 AB/BA 基准。

## Metrics 优化的版本配对对照

```bash
# baseline 是优化前提交 87a126e 的独立镜像，须提前构建并加载，不能覆盖。
make metrics-compare EXPERIMENT_FLAGS='--pairs=4'
# 完成构建/加载后，单独测量：
./bin/kno-experiment --mode metrics --context kno-dev --duration=30s --pairs=4 \
  --baseline-agent-image kubenode-observer/kno-agent:cost \
  --candidate-agent-image kubenode-observer/kno-agent:metrics-cache
```

两版本都使用 200m limit / 100m request、相同内存配置与 workload Pod。每个 case 新 Agent、settle 5s，确认 churn 正在运行并预热 3s；持续抓取 30s，每次开始间隔至少 1s、同一时间最多一个请求，慢请求不累积追赶队列。不采集 CPU profile，不发 accuracy 或 runqlat 请求；仅窗口前后读取 diagnostics，以 cgroup CPU 时间/实际窗口墙钟时间计算 CPU rate。旧版每次 scrape 线性遍历，新版默认 15s 后台刷新；窗口长度至少 20s，必须纳入后台刷新成本，不能只比较缓存命中延迟。

单次 `clientSeconds` 为客户端 GET、完整响应接收和原始文件/命令记录的总墙钟耗时，包含 port-forward 和落盘成本，不是精确服务端耗时；服务端可用 `metrics.scrape` 阶段均值交叉验证。P50/P95 用 nearest-rank，样本少时不解释为稳定服务 SLO。报告各 case、配对百分比变化的中位数及固定 seed 的探索性 bootstrap；CPU 比较按实际窗口归一化，不因慢请求使窗口略长而直接比较 CPU 秒。

每次 scrape 原文、cache 就绪 source 数/最大年龄/错误、生命周期丢失计数、实际 image ID、同 UID/零重启状态、恢复状态和源码归档均保存。baseline 无缓存指标时 source 数为 0；candidate 必须提供三组 source。`collectionErrors` 或缓存未就绪不会使实验数据被丢弃，应在报告中单独解释，不能将流程完成视作质量门限通过。summary 内的 workload 吞吐来自覆盖预热/测量/收尾的整个有限负载，**不是精确 scrape 窗口的业务吞吐**，不用于声称业务收益。

对候选镜像运行原有精度/开销实验可显式指定 `--test-agent-image=kubenode-observer/kno-agent:metrics-cache`。只复制并替换临时 Agent 镜像、设为预加载，不改变原部署，结束时仍恢复原 DaemonSet。

完整功能与 churn 验收可运行 `VERIFY_OUTPUT=log/<新目录> bash hack/verify-metrics-cache.sh`。脚本占用同一实验锁，临时使用已加载的候选镜像、保持 debug 关闭，执行 integration 和两轮共 40000 次子进程 churn，保存指标与 Pod 状态并恢复原镜像。不要与性能测量或其他集群实验同时运行；SIGKILL/断电仍需人工按保存的 original 状态恢复。

## 内存预算观测

```bash
make image-local IMAGE=kubenode-observer/kno-agent:memory
make experiment-build
minikube -p kno-dev image load --overwrite=true kubenode-observer/kno-agent:memory
./bin/kno-experiment --mode memory --context kno-dev --duration=1m \
  --memory-pulses=5 --memory-idle=1m --memory-interval=5s \
  --memory-go-limits=original,80MiB --output log/experiments/<新目录>
```

同一个诊断镜像，两次新 Agent，临时固定 128 MiB memory / 200m CPU limit、32 MiB / 100m request。`original` 保留原配置的 GOMEMLIMIT（如有），另一 case 显式覆盖为 `80MiB`；不是两版不同代码。每次连续执行 5 个有限的 60s churn 子任务并观察 60s idle，约 6 分钟/配置。pulse 之间存在控制请求间隙，不能称为严格恒定到达速率；不强制 GC，heap profile 使用 `gc=0`，所有 profile 解析都在负载结束后执行。失败/中断保留部分样本并恢复原配置，Agent cgroup 改变、重启或 OOM 都会使实验失败。

每 5s 保存完整 diagnostics 和 metrics，区分：RSS；HeapAlloc/HeapInuse/HeapIdle/HeapReleased/StackInuse；Go managed 估计 `Sys-HeapReleased` 与当前软限制；实际 Agent cgroup 的 `memory.current/max/peak/stat/events`。`memory.peak` 不支持时省略而不是返回零，关键计数缺失时报错。Go 的 GOMEMLIMIT 不覆盖所有进程/内核内存，也不是硬上限；kernel/slab/anon/file 字段有包含关系，map memlock 也不能简单加到 cgroup current 上。不同文件非原子读取，单次采样可能错位。

`memory.peak` 为新 cgroup 自创建以来的高水位，不是每个 phase 的独立峰值。`gcCpuFraction` 也是进程生命周期累计口径，不能拿相邻差直接当窗口 GC CPU 百分比。实验只是有限窗口的工程观测，不证明泄漏不存在或生产环境永不 OOM；默认部署是否调整须依据实测余量、GC/CPU 代价和更长观察，不因给了 soft limit 就宣称预算已解决。
