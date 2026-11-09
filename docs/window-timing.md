# M10b：先核对时钟域，再记录真实读取括号

本轮交付的是跨层窗口分析的输入和诊断工具，**还没有交付可信的 Host/Guest 窗口重叠计算**。M10a 的 `exchange-only` 校准保持原意；不能给一个整段资源结果减去校准中点，就称为已经同步。

## 新增字段怎么读

默认资源查询不变；显式请求才返回一个 `timing` 对象：

```bash
./bin/kno top workloads -n <namespace> --duration=2s --timing
./bin/kno guest resources --sandbox <完整64位ID> --duration=2s --timing
# 对应两个资源 API 的 timing=true；false 或省略都不返回 timing。
```

| 字段 | 含义 |
| --- | --- |
| `timing.clock` / `identity` | Linux CLOCK_MONOTONIC 及读取进程看到的 boot ID、time namespace |
| `timing.start.cpu` / `end.cpu` | 两次实际 CPU `Snapshot()` 调用的 beforeNs/afterNs |
| `timing.start.runqlat` / `end.runqlat` | 两次实际 runqlat `Snapshot()` 调用的 beforeNs/afterNs |

全部纳秒是正 int64 十进制**字符串**，不经过 JavaScript 浮点数。顺序必须是 CPU 起点、runqlat 起点、CPU 终点、runqlat 终点，各调用内 before ≤ after，各调用之间不倒退。开始/结束检查时钟身份一致；Guest boot ID 还必须与 Probe 身份一致。读取错误、倒退或可检测的身份变化使该次请求失败，不把坏 timing 丢掉后返回“成功”。

四个括号各包住原来的那一次读取，不新增/重试 map 扫描，不清零计数器，不修改 CPU 分母。默认关闭时不额外读钟或读取时钟身份。Host `durationSeconds` 改用保留 Go monotonic 信息的本地起点计算，避免对 UTC 序列化起点使用墙钟差；这不改变 CPU 自身 `windowSeconds`。

特殊情况：Host 无匹配行时没有读取 map，立即返回空列表，**不伪造 timing**；Guest 即使 comm 筛选为空仍执行原来的快照窗口，因此可返回 timing。只有字面量 `true`/`false` 有效；重复、空值、`1`、`True` 和格式错误 query 均为 400。Node 仅在请求 true 时向 Probe 转发；请求 timing 的旧 Probe 不兼容，需要一起升级，缺失或不合法 timing 为 502。接口定义复用 [共享 schema](../api/window-timing.schema.json)、[Host OpenAPI](../api/host-resources.openapi.yaml) 和 [Guest OpenAPI](../api/guest-resources.openapi.yaml)。

这些括号**不意味着**：

- map 是原子的；CPU 读取本身包括累计/明细等不同读取，runqlat 也有多个 map。
- procfs 内存、IO 或身份读取具有相同窗口，或部分/缺失行因此变得完整。
- 所有调度等待都起止于该窗口。runqlat 按完成等待入账，等待开始可能早于起点。
- Host 和 Guest 的纳秒可以直接相减。它们仍属于不同 boot/时钟域。

同域同类计数器的两次读取分别落在 `[s0,s1]`、`[e0,e1]`，其读取间隔可用内界 `[s1,e0]` 与外界 `[s0,e1]` 描述；这是读取时间的保守描述，不是完整事件集合或因果结论，更不能按区间比例切分已有 histogram。

## BPF 与用户态时钟如何核对

新增独立 `kno-clockcheck`，只在明确运行时加载一个私有 Array map 和 SocketFilter 程序。通过 cilium/ebpf 的 `Program.Test` 同步触发 `bpf_ktime_get_ns()`，再读取私有值；**不 attach、不 pin、不修改现有 scheduler 程序或业务 map**，退出关闭两种 FD。该执行路径见 [依赖库 v0.17.3](https://raw.githubusercontent.com/cilium/ebpf/v0.17.3/prog.go)，helper 的单调时间实现见 [Linux v6.6](https://raw.githubusercontent.com/torvalds/linux/v6.6/kernel/bpf/helpers.c)。它不是调度事件的精度/开销基准。

每份数据保存 `beforeNs → kernelNs → afterNs`，所以用户态减 BPF 的相容偏移区间为 `[beforeNs-kernelNs, afterNs-kernelNs]`。零落在括号内只能说明**该样本与零偏移相容**，不是读钟精度为零或永久映射。另保存 `rawBeforeNs → monoNs → rawAfterNs`，离线脚本以首末点和读取括号计算观测到的 MONOTONIC/RAW 平均速率比，不拟合、不外推、不自动当成后续漂移上界。

Host 有 time namespace 时，正对照在**新建子进程命名空间**设置 +2 秒 monotonic 偏移，要求每份区间包含 2,000,000,000ns，而不包含零。初始 Host 时钟不变。不同 inode、`timeNamespace=unavailable` 或相同 uname 都不能直接推出零偏移；time namespace 行为见 [内核实现](https://raw.githubusercontent.com/torvalds/linux/v6.6/kernel/time/namespace.c)。本轮短时诊断 Pod 与资源窗口 Pod 是不同 VM/boot，不能将前者的零偏移相容性自动应用到后者；后续若需要 BPF↔用户态映射，必须核对同代身份和时效，而不是只按相同 uname 复用。

工具还只读记录 clocksource、kernel cmdline、namespace offsets 和 `adjtimex` 首末参数。`Modes=0` 仅查询，不设置时间；参数单位见 [Linux man-pages](https://raw.githubusercontent.com/mkerrisk/man-pages/master/man2/adjtimex.2)。MONOTONIC 会受渐进调整，RAW 不受同类调整，见 [clock_gettime 文档](https://raw.githubusercontent.com/mkerrisk/man-pages/master/man2/clock_getres.2)。RAW 在这里不是已知 UTC 真值；也没有证明两台内核的 RAW 同速。

## 本地复现与证据边界

先准备 Linux x86_64、BPF 权限、Host time namespace/unshare、免交互 sudo，以及现有本地 Minikube 和 `kno-kata-qemu-guest` debug handler。Python 检查依赖 jsonschema（本地 4.10.3）、PyYAML，不额外安装 referencing。所有镜像短名称、scratch 本地构建/加载、Pod `imagePullPolicy: Never`，不拉 Docker Hub：

```bash
GOFLAGS=-buildvcs=false make clockdomain-test
GOFLAGS=-buildvcs=false make m10-window-test
go test -race ./internal/clockdomain ./internal/guest ./internal/host ./cmd/kno
python3 -m unittest discover -s hack -p clockdomain_test.py
```

`GOFLAGS` 只用于有庞大参考目录的本地构建；源码 commit、文件/二进制 hash 和镜像指纹另存。构建在采样之前完成。Harness 拒绝已有测试 namespace/实验锁，只删除自有资源；窗口测试临时替换 Agent，退出恢复原 spec 并比较。需要在其他环境发布数据前检查 kernel cmdline/Pod 元数据是否含敏感信息。

时钟工具先测 Host、+2秒子 namespace，再分别运行 runc/Kata 短时 Pod。窗口测试等负载打印 worker ready 后固定预热5秒，保存 3 组 **前校准 → 并发 Host/Guest 两秒窗口 → 后校准**，验证 schema、read order、相同 Pod/Sandbox/boot/Probe instance、校准包围窗口、旧请求和 Host 空选择；每轮要求存在有效 CPU 行，不自动重试或忽略坏轮次。所有尝试都保留。Postman simulation 验证真实 Relay 的转发/拒绝路径，不代替 Guest 内核或网络测试。结果与尝试见 [本轮报告](../log/2026-11-09-clockdomain-windows.md)。

## 下一步明确做什么

M10b 后半段实现离线的**有条件**窗口映射：调用者明确给出可接受的速率变化模型/上界，利用前后校准和读取括号计算重叠区间；身份不一致、模型不相容或无法证明足够重叠则拒绝比较。不能从两点拟合残差很小反推出真实上界，也不能自动放宽到通过。当前数据发现 Host 调整参数会变化，尤其不能静默默认零漂移。

之后 M10c 才做预定顺序和质量门限的 Host contention × Guest contention、runc/Kata、Probe off/on 研究。完成本地验收、可移植配置和版本冻结后才到 tdk 做正式实验；本轮没有连接或改动 tdk。
