# M10b：时钟域诊断与资源读取括号

本轮完成 M10b 的诊断和输入采集部分，**没有完成跨域窗口映射/重叠，也没有提前开始 M10c 因果实验**。所有集群操作限本地 `kno-dev`，没有连接或修改 tdk。基线为已合入 M10a 的 `fb89138392e6f4356bc4449a8775af2b055ad5e5`；捕获时工作树包含本 MR 的实现，实际 Go 文件、二进制和镜像指纹随运行保存，不把基线 SHA 冒充新实现版本。

## 思路与实现

M10a 已发现全程固定 offset 不相容，所以先核对读钟对象，不能直接把最窄校准的中点外推到整段 eBPF 窗口。

新增独立 `kno-clockcheck`：一个私有 map + SocketFilter TEST_RUN，读取真实 `bpf_ktime_get_ns()`，由用户态 MONOTONIC 包围；另用 RAW 包围 MONOTONIC。无 attach/pin，不更改现有 exec/CPU/runqlat BPF。`adjtimex(Modes=0)` 只查询参数，不设置时间。+2秒正对照只作用于 `unshare --time --fork` 的新子命名空间。

资源接口/CLI 仅在显式 `timing=true` / `--timing` 时增加一个 timing 对象：boot/time namespace，以及 CPU/runqlat 的起点/终点实际 Snapshot 读取括号。默认不读新时钟、不新增字段，四次原 map 读取数量和 CPU/histogram 记账不变。Node 转发该选项并拒绝缺失、倒退、乱序或 boot 身份矛盾的数据。同步更新两个 OpenAPI 和共享 schema；Host 格式错误 query 现在明确400，`durationSeconds` 保留 Go monotonic 差值，CPU `windowSeconds` 不变。字段解释见 [指南](../docs/window-timing.md)。

## 时钟证据：观察到了什么，没有证明什么

环境仍为 WSL2 Linux6.6.114.1、Docker-driver Minikube/K8s1.35.1、Kata4.3.1 Go/QEMU11.0.1、官方 debug Guest6.18.56。Host clocksource=tsc；本次 Guest=kvm-clock。没有调整任一时钟、clocksource 或 Kata runtime 配置。

先做的探索诊断保留在 [原始目录](m10b-20261109/)，再用提交的 harness 独立重跑，保留在 [可复现诊断目录](clockdomain-m10b-20261109/)。两次均保留全部样本，不根据观测值筛选。

| 运行 / 时钟域 | 样本数 | 首末 MONOTONIC/RAW 观测速率比，ppm（约） |
| --- | ---: | ---: |
| 探索 / WSL Host | 20 | −3348.414 .. −3347.635 |
| 探索 / runc，同 Host 内核、不同时段 | 50 | +984.320 .. +984.513 |
| 探索 / Kata Guest | 50 | −0.075 .. +0.071 |
| 独立重跑 / Host | 20 | −2370.819 .. −2370.274 |
| 独立重跑 / runc | 50 | −3804.451 .. −3804.274 |
| 独立重跑 / Kata Guest | 50 | −0.119 .. +0.083 |

表中范围来自读取括号和首末差值，是有限区间的平均速率比，不是置信区间、瞬时频率或未来漂移界；RAW 也不代表已知 UTC 真值。Host 与 Guest 数据顺序采集，不能据此认定二者 RAW 同速或计算同一时刻的 Host/Guest 漂移。

探索 runc 的首末 `adjtimex` 参数确实变化：`frequencyScaledPpm` −956486→1190280，`tickUs` 9965→9963。Guest 首末 frequency=0、tick=10000、status=64。这给“Host 时钟调整是此前偏移变化的可能机制”提供了直接线索，**不证明它是 M10a 现象的唯一原因，更不支持写死一个固定速率修正**。没有猜测是哪个服务实施调整。

独立重跑的 Host20、runc50、Guest50 个样本全部满足用户态−BPF括号包含零；5个子 namespace 样本全部包含 +2,000,000,000ns 且不含零，正对照 envelope 为 `[1999950828,2000061889]` ns。这只验证采样时的相容性，不称为精确零偏移或跨 VM 绝对精度。短时诊断 Pod 与资源窗口 Pod 的 Guest boot 不同，**没有将这个诊断跨 VM 复用为窗口的 BPF 映射**。退出码0，测试 namespace/lock 已清除。首轮探索的125个样本也分别通过相应检查。

## 真实 Kata 窗口与失败尝试

首次窗口运行保留在 [window-m10b-20261109](window-m10b-20261109/)，退出码1，不能当作通过：

1. 采完三轮后，校验器导入本机未安装的 `referencing` 失败。改为复用已安装 jsonschema4.10.3 的本地 RefResolver；外部 schema 仅从预注册本地文件解析，拒绝网络查找，未安装新依赖。
2. 用修正校验器复核原数据，又发现第一轮 Guest CPU/runqlat 为 null：原始 issues 明确记录线程集合10→11。原先只等待1秒且未确认 worker ready，准备不足。API 正确保留 partial/null，没有把缺失补零；验收也没有为此放宽。

将准备步骤固定为**等 worker ready 后预热5秒**，同一镜像、不改统计实现，独立重跑至 [window-m10b-20261109-r2](window-m10b-20261109-r2/)。不是从旧数据里挑三轮，也没有 retry-until-valid。新的3轮全部一次通过原 CPU 有效性门限。

每轮保存前7样本校准 → 并发两秒 Host/Guest 资源查询 → 后7样本校准。离线验证9份资源响应（6份有 timing，2份默认请求，1份 Host 空选择）、6份校准；校验 JSON schema、int64 字符串、读取顺序、相同 Sandbox/Pod UID/Probe instance/两端 boot+namespace，以及同域前后校准包围整个 map 读取区间。每轮 Host 和 Guest 均有有效 CPU 行；没有以 P95 大小挑选。

Host 四个读括号的最大宽度为100,933,730ns（约101ms），Guest最大39,537,234ns；这包含读取和调度等耗时，不是专属 map syscall 成本或精度承诺。也说明不能把非原子读取当成一个时刻。原始 histogram、partial/issues 和括号都保留，**未报告跨域重叠率或因果结论**。

成功运行结束 Agent Pod 未重启，events lost=0，exec/cpu/runqlat 三组 map stats ready=1、refresh errors=0。两次窗口尝试均删除自有 namespace/lock、恢复原 Agent spec 并逐项比较一致；最终退出码0。构建在实验前完成；环境仍为共享开发机，功能 smoke 不是性能基准。

## 检查与可复现入口

- `go test ./cmd/... ./internal/... ./examples/...`：所有维护包通过；本地 `go test ./...` 因遍历庞大未跟踪 `ref-project` 未产出结果而中止，不记为通过，未改动该目录。CI 在干净 checkout 跑原有 `go test ./...`。
- `go test -race ./internal/clockdomain ./internal/guest ./internal/host ./cmd/kno`：通过。包含默认不读钟/不加字段、四次读取各一次、>2^53精度、读钟失败/身份变化/倒退/乱序、不合法 query、空选择和 Relay 转发/拒绝。
- 显式特权 `TestKernelClockBracketAndLifecycle`：真实 BPF TEST_RUN，10次加载/读取/关闭，检查两种 FD 均关闭，通过；普通 CI 明确跳过该特权项。
- `python3 -m unittest discover -s hack -p clockdomain_test.py`：2项通过，包含+2秒正对照和损坏/溢出/浮点输入拒绝。
- Postman CLI1.86.0 本地 simulation：运行生产 Relay/Clock Relay，不替换被测代码。22个子用例通过，24次 Guest 依赖交换均命中唯一 mock，无 passthrough。覆盖新增 timing 成功/缺失/乱序/boot矛盾，以及旧成功、429、JSON/HTML错误、identity、nonce、过大响应、redirect、timeout。读取实际代理日志核对路由，结束停止自有进程；原始代理日志在忽略的dist中，不公开提交。该结果不证明 Guest 调度精度或 Pod 网络，后者由上述真实 Kata smoke 单独覆盖。
- `go vet ./cmd/... ./internal/... ./examples/...`、shell语法、源码/二进制 hash 复核通过；`make clockdomain-test`、`make m10-window-test` 提供同一复现入口。镜像为短名称、本地 scratch 构建/加载、Never，不拉 Docker Hub。

Go/内核/模拟测试输出与构建记录见 [检查目录](m10b-20261109/)，正式可复现诊断与独立成功窗口分别有 source-head、source-sha256、images 和 exit。故障模拟的契约先行/真实调用验证由本轮 Postman 技能要求纳入，不涉及账号绑定、云 mock 部署或 workspace 发布。

## 尚未完成与下一步

M10b 后半段：带**显式速率变化假设**的离线窗口映射/重叠；无法满足假设或身份/覆盖门限时拒绝比较，不自动扩大速率上界直到通过。用户没有给定上界，本版不猜一个默认值，也不把有限读钟结果当作通用保证。

随后再做 M10c Host×Guest 干预、runc/Kata、Probe off/on，最后完成可移植环境检查、本地验收和版本冻结后才到 tdk 正式实验。当前不宣称项目已开发完毕，也不扩展到逐事件 trace、VSOCK、Web UI 或调时系统。
