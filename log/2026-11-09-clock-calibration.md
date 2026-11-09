# M10a：用户态时钟校准基础与真实 Guest 验证

2026-11-09，基于已合入的 M9（main `889908e`），本轮只完成 M10a，不将整个 M10 或项目开发标记完成。**按用户约定，当前仍在本地 Minikube 开发验证，开发收尾后才到 tdk 正式实验；本轮没有连接或改动 tdk。**

设计、字段和复现见 [校准指南](../docs/clock-calibration.md)，接口见 [OpenAPI](../api/guest-clock.openapi.yaml)。实现提交 `0db7ea8`，dry-run 解析修正 `3b17b2e`；live 镜像/文件 hash 和 source HEAD 单独保存。之后的变更是补充测试、离线诊断、文档和证据，没有更改 live 的时间戳路径。

## 本轮交付

- Probe 新增 `/api/v1/guest/clock`，通过 Linux CLOCK_MONOTONIC 读 g2/g3；Node 读取 h1/h4 并关联当前 Sandbox。
- 默认 7 次（3..15），每次新 nonce，复用 M9 的 opt-in/fixed Pod IP/port、无代理/重定向、Pod UID/VMM generation 校验。burst 内 boot、time namespace、Probe instance 改变就拒绝，不默默重试或混合不同代际。
- 保存每次 `[g3-h4,g2-h1]` 及中点、四个原始时间戳，选首次最小残余宽度。NS 均为 int64 十进制字符串，避免浮点精度损失。
- 明确 `scope=exchange-only`；不设置系统时钟，不推断全窗口 offset 恒定、不估计置信区间/未来 TTL，不宣称 BPF/资源窗口已经对齐。
- 单 RPC 1s、总预算 5s、Probe 响应 8KiB；未知/尾随 JSON、错误媒体类型、缺失时间戳、时钟倒退/矛盾失败。默认 relay 仍关闭，Node 与 Guest resources 共用单操作 gate。

本轮没有改 BPF C、生成对象、map 容量、Host/Guest CPU/runqlat 计算、已有 API 字段或默认部署；没有自研时间同步协议。方法采用 [RFC 5905](https://www.rfc-editor.org/rfc/rfc5905.html) 的四时间戳思想，但没有复制 NTP 的协议/纪律/精度承诺。边界推导和恒定 offset 假设在指南中明确区分。

## 环境与先导失败

- WSL Host Linux `6.6.114.1-microsoft-standard-WSL2`，Minikube kno-dev / Docker driver / Kubernetes 1.35.1、containerd 2.2.1、Kata 4.3.1 Go/QEMU 11.0.1。
- 使用 M9 已安装的官方 debug Guest kernel 6.18.56；[kernel/config hash](m10-20261109/guest-kernel-sha256.txt) 和 [runtime types](m10-20261109/runtime-types.json) 另存，没有改 runtime handler。
- Host 支持 time namespace，Guest [config](m10-20261109/guest-time-ns-config.txt) 为 `# CONFIG_TIME_NS is not set`。Probe 输出 `unavailable`，不伪装成已测量零 offset。Linux 的虚拟时钟/namespace 关系见 [man-pages 源文档](https://kernel.googlesource.com/pub/scm/docs/man-pages/man-pages/+/master/man/man7/time_namespaces.7)。本轮并未核对 BPF ktime 与用户态 namespace offset。
- 所有项目镜像短名称、scratch 本地构建/Minikube image load，无 Docker Hub 拉取。使用独立 `:clock` Agent/Probe tag，不覆盖 M9 Probe `:dev`。本地构建显式 `GOFLAGS=-buildvcs=false`，源码以提交/文件 hash 留证，不依赖 build info；正式采样前构建完毕，无并行 Go 编译或 Postman simulation 干扰。
- 首次 dry-run [失败输出](m10-20261109/preflight.txt)：kubectl 多文档 `create --dry-run -o json` 返回多个独立对象，最初当作 List 读取导致 `Cannot iterate over null`。改为 jq slurp 构造 List，[只读预检通过](m10-20261109/preflight-final.txt) 后才运行 live；失败发生在任何实验部署前，没有删除失败记录。

## 数学、真实 HTTP 与模拟验证

全部使用生产 Handler/Relay，不以 mock 取代被测校准器。

- [Go 全模块测试](m10-20261109/go-test.txt)、[最终测试](m10-20261109/go-test-final.txt)、[vet](m10-20261109/go-vet-final.txt)、[race](m10-20261109/go-race-final.txt) 通过。
- 数学测试：1000 个固定 seed 非对称延迟/正负 offset 样本、超过 2^53 的传输往返、接近 int64 边界、负值/倒退/负残余拒绝，及 disjoint exchange 区间不被强制合并。
- [同内核零偏移控制](m10-20261109/same-kernel-control.txt)：真实 Linux ClockSource + loopback HTTP +生产 ClockHandler/Relay，三次已知 offset=0 均在区间内。它验证实现和读钟，不是 Guest 未知偏移的真值标定，也不是实际 runc Pod 时钟隔离试验。
- 真实 loopback HTTP 故障覆盖：UID/nonce/domain、Probe/boot/namespace/Host namespace 换代、VMM generation/删除、两端倒退、矛盾时间戳、缺失/未知/尾随/超大/坏 JSON、坏 content type、503 JSON/500 HTML、429、重定向、超时、Host 读钟失败、严格 query、默认关闭和共用 gate。
- [Postman 本地 simulation 测试](m10-20261109/simulation-tests.txt)：新 Clock 9 个 case、旧 Guest resources 9 个 case，成功时钟 burst 发 3 次，其余各一次；实际 proxy log 观察到 Clock 11 次、resource 9 次，全部 destination=guest，无 passthrough/静默 member。覆盖成功、身份/nonce、429、JSON/HTML 错误、超大、拒绝跟随重定向、超时。摘要见 [simulation-validation.json](m10-20261109/simulation-validation.json)。

Postman 技能用于固定契约和独立依赖故障证据；只在本地运行仓库自有 mock，没创建账号/上传 workspace/发布服务。原始 simulation logs 留在 ignored dist，不提交；运行后停止确切的自有进程。mock 的 hrtime 不是真实 Guest 真值。

## 真实 Kata：85 个往返样本

一台真实 Kata VM：5 组 idle，每组 7 次；启动原有 cpu-contention 程序（Guest 8 个 worker 固定同一个 Guest CPU、15s），再测 5 组各 7 次。删除 Pod，旧 SID 404；同名重建后新 UID/SID/Guest boot/Probe instance 全部变化，再测 15 次。**无按宽度或预期结果重跑整次实验**；每个 burst 保留所有 exchange。

结果：[live exit=0](clock-final-20261109/exit.json)，[原始数据/契约验证](clock-final-20261109/validation.json)。11 个 Calibration、1 个 direct Probe 响应、2 个 disabled/deleted 错误均通过 schema/精确整数算术/排序不变量。各最窄区间宽度（不是单边误差，不是真值偏差）：

| 分组 | 1 | 2 | 3 | 4 | 5 |
| --- | ---: | ---: | ---: | ---: | ---: |
| idle，µs | 1245.418 | 957.359 | 912.344 | 826.598 | 1285.111 |
| loaded，µs | 912.270 | 867.228 | 902.794 | 1322.569 | 913.499 |

重建后的 15 次样本最窄宽度 956.027µs。这不是“测到 offset 准确到 1ms”，而是在模型假设下得到的区间宽度；中点最多只是展示约定。没有随机化顺序、paired control 或统计显著性结论，不宣称负载使校准更好/更坏。

### 不能忽略的模型兼容性发现

每个 7 次 burst 的 Host 跨度约 0.13–0.14s；单个 burst 的区间没有排斥一个共同固定 offset，但这不证明真实 offset 恒定。

若额外假设整个 idle 阶段 35 次交换共用一个 offset，区间约束不兼容，gap=1,746,127ns；loaded 阶段对应 gap=2,876,689ns。五组记录的 Host 跨度各约 0.89/0.86s，首末所选中点变化约 +3.011/+3.038ms。离线诊断见 [validation-with-model-check.json](m10-20261109/validation-with-model-check.json)。这个交集诊断**不是校准器合并区间的算法，也不决定测试接受与否**。

数据不支持把一个固定 offset 套用到整个实验阶段。它仍不能独立证明具体时钟频率/漂移率、NTP、虚拟 clocksource 或哪层调度是原因；时间戳模型/虚拟化假设也必须检查。M10b 先调查这些因素、BPF 时钟域和误差增长，再承诺任何窗口映射，不能仅把两条曲线平移对齐。

## 资源与兼容性

- Probe 500m/128MiB，inspect 时 cgroup current/peak 均 92,041,216 bytes（87.78MiB），max/oom/oom_kill=0；nr_throttled=6、throttled_usec=370817。inspect 包含额外诊断进程，`ready:false` 是该不加载 BPF 的诊断模式，不是 daemon 健康失败；不作开销或长期 OOM 保证。
- 同一最终 Probe 的 [Guest resource](clock-final-20261109/guest-resources.json) 窗口实际 CPU=105.97%，runqlat count=11638、覆盖 15/15；[Host resource](clock-final-20261109/host-resources.json) 仍独立返回。两次查询顺序执行，不能称同步窗口。
- [最终 Node 镜像 runc integration](m10-runc-20261109/exit.json)：lifecycle、CPU hotspot、runqlat 全通过，Agent 同 UID、restart=0、kernel eventsLost=0、三个 map source ready/refresh errors=0，原 spec canonical compare 恢复。本轮没有再跑 40000/churn 或新的 off/on 成本基准；BPF/记账没变，历史 M9 证据不冒充本轮重测。
- [结束状态](m10-20261109/final-state.json)：Node Ready、原 Agent spec 恢复、实验 namespace/Pods/锁清理、无 Ready Kata Sandbox。只读检查流程见 [check-state.sh](m10-20261109/check-state.sh)；tdk 未使用。

## 后续交付边界

M10a 是用户态校准基础和反例收集，不是事件时间对齐完成。下一阶段 M10b 核对虚拟 clocksource/速率、BPF ktime 与 namespace、真实快照括号、窗口误差；M10c 才做受控双轴 contention、runc/Kata 与 Probe off/on。项目开发和本地验收收尾、冻结版本后，再在 tdk 独立做正式实验，保存该环境自己的配置和数据。
