# M10a：先校准用户态时钟，不冒充跨层事件对齐

M9 已能分别查 Host/VMM 与 Guest 进程，但两份 `startedAt` 不能直接叠成时间线。本轮先实现独立的四时间戳往返校准，**不修改系统时钟，也不把 offset 当成根因结论**。M10 尚未整体完成，后续还需 BPF 时钟域核对、测量窗口对齐和受控双轴实验。

## 用什么时钟

两端直接调用 Linux `clock_gettime(CLOCK_MONOTONIC)`，不是 `time.Now().UnixNano()`，也不是序列化后已丢失 Go monotonic 信息的 `time.Time`。Boot ID、time namespace 和 Probe instance 一起标记时钟/进程代际；不能用 Pod 名字或 Guest uname 判断是否同一 VM。

Linux time namespace 可虚拟化单调时钟，见 [Linux man-pages 源文档](https://kernel.googlesource.com/pub/scm/docs/man-pages/man-pages/+/master/man/man7/time_namespaces.7)。本地 Host 有该功能，官方 Guest debug config 的 `CONFIG_TIME_NS` 未设置；不存在的 `/proc/self/ns/time` 返回 `timeNamespace: unavailable`，**不是自动认定为零 namespace offset，更不能据此声称 BPF ktime 与用户态已对齐**。

## 四个时间戳与区间

```text
Host h1：发 HTTP 前 ───────────→ Guest g2：处理请求时
                                      │ identity 读取
Host h4：读完响应体 ←─────────── Guest g3：编码/发送响应前
```

这是 HTTP/用户态括号，不是 NIC 硬件收发时间。连接、排队、序列化、网络和两端调度耗时均包含在括号中。沿用 [RFC 5905 的四时间戳思想](https://www.rfc-editor.org/rfc/rfc5905.html)，但不实现 NTP 协议、时钟纪律或其精度保证。

定义 `offset = Guest - Host`。在**单次往返中 offset 恒定、路径延迟非负**的条件下，直接由时间先后约束得到：

```text
lower = g3 - h4
upper = g2 - h1
width = (h4 - h1) - (g3 - g2)
midpoint = lower + floor(width / 2)
```

这是本项目从四时间戳约束做出的区间推导。中点不是真值，路径非对称时会偏移；误差不能凭多测几次就宣称趋近零。独立 Guest/Host 时钟速率变化、暂停/恢复等可能使恒定 offset 假设不成立。倒退、负残余宽度、身份变化等检测到的矛盾会失败，但通过不证明所有未建模漂移都不存在。

默认顺序采样 7 次（3..15），间隔 20ms，保留每次原始值；选择**残余宽度最小的第一份样本**，不按 offset 大小选。不同往返对应不同时刻，不能假设 offset 不变后强行取交集或平均。本版不计算漂移率、置信区间、缓存 TTL，也不将校准结果外推到未来或整个 eBPF 采样窗口。`scope=exchange-only` 特意提醒调用者这一点。

## 接口与字段

```bash
./bin/kno guest clock --sandbox <完整64位ID> --samples=7
```

Node 的 `/api/v1/guest/clock?sandbox=...&samples=7` 返回校准 burst；直接 Probe 同路径不接受 query，只返回一份 g2/g3 响应。复用 M9 的 opt-in Pod IP 固定端口、字面量地址、无代理/无重定向和当前 Sandbox/VMM 校验。Node 生成每次新 nonce，检查前后及 burst 内 boot/namespace/Probe instance；不允许 Guest 自报 Sandbox 身份。仍无 TLS/auth/加密 attestation，只适合可信研究环境。

| 字段组 | 为什么保留 |
| --- | --- |
| `host` / `guest` / `probeInstanceId` | 分开标记内核启动、time namespace 与 Probe 进程代际 |
| `samples[].hostSendNs/guestReceiveNs/guestSendNs/hostReceiveNs` | 原始 h1/g2/g3/h4，可独立重算，而非只相信一个 offset |
| `offsetLowerNs/offsetUpperNs` | 显式暴露不确定区间，不给出伪精确值 |
| `offsetMidpointNs` | 区间中点便于展示，不是已知真值 |
| `selectedSample` | 指向首次最窄样本，其他样本没有被删掉 |
| `scope=exchange-only` | 防止把本次往返估计默认为后续窗口的有效映射 |

所有纳秒字段是 **int64 十进制字符串**，包括可能为负的 offset；避免 JavaScript Number 超过 2^53 的精度损失。已有 Guest resource API 的数值和字段保持不变。

每次依赖调用最多 1s，整个 burst 预算 5s，Probe 响应最多 8KiB；不自动重试或跳过坏样本。校准与 Guest resources 共用 Node 单操作 gate，忙则 429；直连 Probe clock 是轻量端点，不加 CPU measurement mutex。400/404/405/409/429/502/503/504 的边界与 JSON 错误见 [OpenAPI](../api/guest-clock.openapi.yaml)，所有响应 `Cache-Control: no-store`。JSON 响应多余字段、尾随对象、缺失时间戳、错误 content type 均明确失败。

## 本地复现

先按 [M9 指南](guest-probe.md) 安装官方 Kata/debug handler。本轮仍只使用本地 Minikube：

```bash
go test -race ./internal/guest ./cmd/kno
go test ./internal/guest -run TestClockSameKernelControl -v
CLOCK_OUTPUT=log/clock-my-run make m10-clock-test
python3 hack/validate-clock-contract.py log/clock-my-run
```

若工作树含很大的无关参考目录，可对本地构建显式设置 `GOFLAGS=-buildvcs=false`；源码提交与文件 hash 仍由实验 harness 单独记录，CI 不需要该选项。镜像为 `kubenode-observer/kno-agent:clock`、`kubenode-observer/guest-probe:clock`，全为短名称、本地 scratch 构建/加载，不拉 Docker Hub，不覆盖 M9 的 `:guest`/`:dev` Probe tag。

脚本拒绝已有 `kno-clock-test` namespace 或实验锁，先测默认关闭 503，再临时启用。运行 5 个 idle burst、5 个 8-worker Guest 负载 burst，同名 Pod 删除后旧 SID 404，再重建检查 UID/SID/boot/instance 全部变化，使用 15 样本重测。保存原始数据、镜像/源码指纹、资源和指标；结束删自有资源、恢复原 Agent spec 并比较。负载不是随机化配对基准，不宣称其间的不确定度变化是因果效应。

同内核 loopback 控制使用生产 ClockHandler/Relay 和真实 CLOCK_MONOTONIC，已知真实 offset 为 0，要求每份区间含 0；它验证数学、读钟和 HTTP 路径，但不证明 VM 内未知 offset 的真值。Postman 本地 simulation 另测生产调用路径的成功/故障，不能代替真实 Guest 网络测试。最终数据与失败记录见 [报告](../log/2026-11-09-clock-calibration.md)。

## 离 tdk 正式实验还差什么

1. M10b：核对 BPF ktime/namespace offset，记录两端真实快照时间括号和窗口重叠；不只给两次顺序查询加一个 offset。
2. M10c：Host contention × Guest contention 双轴干预、runc/Kata 及 Probe off/on 对照，预先规定质量门限和重复顺序，不按想要的分位数挑结果。
3. 整理可移植配置、环境 Go/No-Go、离线镜像和测试产物，完成本地兼容性验收、冻结版本。
4. **以上开发收尾后才到 tdk 正式实验**：先确认其 runtime/KVM/权限/资源与隔离条件，单独保存环境和数据；Minikube 的结果不充当 tdk 结论。
