# M9：真实 Guest Probe、身份关联与验收

工作跨 2026-11-05/06；Nov 5 的原型失败与首次成功独立保留，Nov 6 使用提交 `dabd2fd` 的最终实现验收。开发/实验在 WSL Ubuntu 的 `kno-dev` Minikube，未在公司共享 tdk 上部署 Kata。实现与字段解释见 [M9 指南](../docs/guest-probe.md)。

## 做了什么、没有做什么

新增 `kno-guest`：在 Kata Pod 的特权 sidecar 内复用已有 CPU/runqlat BPF，借助共享 Pod PID namespace 的 procfs 验证进程 generation。Node 只向当前可信拓扑所指的、显式 opt-in Pod IP 固定端口请求，校验 UID/nonce/域及前后 VMM generation 后加入 Sandbox ID。Guest 没有 Host 挂载、Kubernetes token 或自报 Sandbox 权限；Host 与 Guest cgroup/PID 不交叉索引。

没有修改 BPF C、生成对象、map 容量或普通 runc 统计路径。唯一底层增量是只读查询原有 runqlat `task_identity` map，用于排除 Kata exec 历史 cgroup key。没有 VSOCK、自研 Guest 内核、时钟校准、跨层根因判定，也没有宣称 Probe 零开销或生产安全。

## 环境与必须保留的变更

- Host：WSL Linux `6.6.114.1-microsoft-standard-WSL2`，x86_64/cgroup v2，Go 1.22.2。
- Minikube：Docker driver，12 CPU /4GiB，Kubernetes 1.35.1、containerd 2.2.1。
- Kata：4.3.1 Go/QEMU 11.0.1；官方 debug Guest kernel `6.18.56-208-debug`，uname 同默认内核均为 `6.18.56`。
- 新增独立 `kata-qemu-guest` handler / `kno-kata-qemu-guest` RuntimeClass；runc 仍默认，M7/M8 `kata-qemu` 不变。使用安装包原有 debug kernel，无新内核下载或构建。
- 保留开发环境 runtime 配置；备份 `/etc/containerd/config.toml.kno-before-guest`。RuntimeClass 的 512MiB 是调度 overhead 声明，不是本阶段测出来的实际 VM 开销。
- 镜像均短名称、本地 scratch 构建/加载，测试 Pod 使用 Never；运行时状态中自动补全的 docker.io 字符串不代表拉取过 Docker Hub。

安装原始输出：[install.txt](m9-20261105/install.txt)。最终 Guest kernel/config hash、镜像 inspect、源码文件 hash 均在最终实验目录，不只按 tag 或 uname 认定版本。

## 失败、诊断和调整（没有删除失败数据）

| 尝试 | 发现 | 处理/保留范围 |
| --- | --- | --- |
| Nov 5 capability check | 默认 Guest 无 BTF；debug 非特权被拒；debug 特权成功 | [原始能力检查](guest-check-20261105/exit.json)，专用 debug handler，不改默认内核 |
| pilot 1/2 | relay 503，Informer 没有身份 | [pilot1](guest-pilot-20261105/exit.json)、[pilot2](guest-pilot2-20261105/exit.json)；先修网络，不降级成相信 Guest 自报 UID |
| pilot 3/4 | 同一 namespace PID 有两份历史 CPU cgroup key，不能唯一关联 | [pilot3](guest-pilot3-20261105/exit.json)、[pilot4](guest-pilot4-20261105/exit.json)；增加原有 scheduler identity map 只读筛选，不取第一个/最大计数 |
| pilot 5 | CPU 可测，但某个 idle 线程没有当前 cgroup 的 runqlat 双端点 | [pilot5](guest-pilot5-20261105/exit.json)；增加线程覆盖率，只返回可测子集，不补零 |
| pilot 6 | Go 在窗口中创建 OS thread，严格 CPU 集合检查正确拒绝 | [pilot6](guest-pilot6-20261105/exit.json)；每窗最多三次质量尝试，保留全部，不按延迟大小选 |
| pilot 7 | 首次完整成功，竞争首窗有两次线程变化后第三次有效 | [完整原始数据](guest-pilot7-20261105/exit.json)，仅作先导，不独立宣称统计显著 |
| Nov 6 race | 测试 server 对 calls++ 与测试读取竞争 | [原始失败](m9-20261106/go-race.txt)；测试计数改 atomic，生产 relay 路径不变 |
| Nov 6 capability | `kubectl logs` 合并错误 stderr 与 JSON，导致 jq 失败 | [原始失败](guest-capability-20261106/exit.json)；`--check` 错误只输出 JSON，仍非零退出；最终三对照独立重跑 |

早期 pilot 是迭代中的未提交原型，保留各次采集与部分源码 hash，但不声称能从一个统一提交重现所有旧失败。最终实现使用明确提交与镜像指纹。

### 网络故障不是 BPF 故障

手动启动 Docker 节点后，Node `/etc/hosts` 丢失 `control-plane.minikube.internal`。kube-proxy 的 apiserver 地址错误解析，无法同步，Service VIP `10.96.0.1:443` 不通；Node 直连 `192.168.49.2:8443/healthz` 正常。旧 Host 路径可通过 CRI fallback 工作，但 Guest relay 必须有真实 Informer 身份，因而拒绝请求。

保留原 hosts 到 Node `/etc/hosts.kno-before-m9-network` 及仓库证据，恢复精确的 `192.168.49.2 control-plane.minikube.internal` 映射；确认 kube-proxy 镜像已缓存/IfNotPresent 后只重建该旧 Pod，没有改 DaemonSet 镜像/spec。随后 cache sync、iptables Service 规则及 VIP 健康恢复。最初尝试把文件 docker cp 到 Node `/tmp` 因 systemd 私有 tmpfs 不可见而失败，之后使用 `/etc` 中的专用临时文件成功，所有输出保留。

证据：[hosts 前](m9-20261105/node-hosts.before)、[后](m9-20261105/node-hosts.after)、[proxy](m9-20261105/kube-proxy-after.txt)、[Service 规则](m9-20261105/nat-after.txt)。`repair-network.sh` 只记录当时精确修复，不应重复当安装步骤运行。没有设置持久 hosts 守护服务；应优先正常使用 Minikube 生命周期。

## 测量方法和限制

Guest workload 为原有 Go cpu-contention 程序，改本地文件名为 guest-burn；1 worker →8 workers →1 worker，各持续 60s，workers 固定 Guest 同一逻辑 CPU。每阶段最多八个 1s warmup；每个目标 5s 窗口最多三次，按 CPU 完整稳定线程集、runqlat 有非零观测等质量条件接受，不以 quantile 大小选择。warmup、未接受尝试、接受副本均存档。这样的条件选择只表征稳定/可测子集，不是所有窗口的无偏总体估计。

runqlat 分布允许部分线程：两端存在、单调且身份稳定的线程才纳入；缺失/重置端点不补零。CPU 仍要求整个进程稳定完整线程集合；线程新增/重用/消失使 CPU 和 runqlat 均 null。`task_identity` 是最近 switch-out 的身份，不是当前 cgroup 原子证明；端点间短迁移仍可能漏检。

P95 是 log2 微秒桶上界。两个窗口的 P95 算术平均不是 pooled percentile，也不构成显著性检验。Nov 5 pilot7 的 baseline/竞争/恢复窗口 P95 分别为 `[127,2047]`、`[4095,2047]`、`[127,127]` us；baseline 覆盖 4/5，竞争 18/18，恢复 4/6。该波动促使最终 harness 将“>2倍”从 pass/fail 改为单独的描述性 trend，不因趋势不符而重跑到成功。

Host 查询在每阶段 Guest 窗口之后，**不是同步窗口**，只验证域分离与 Sandbox 关联。没有 Host/Guest clock offset 校准；不能用这组数据直接区分 Guest contention、Host vCPU 等待和 Probe 自身干扰。

## 可重复验证

```bash
go test ./...
go vet ./cmd/... ./internal/... ./examples/exec-test ./examples/cpu-hog ./examples/cpu-contention ./examples/observer-benchmark
go test -race ./internal/guest ./internal/topology ./internal/host ./internal/probe
sudo env KNO_BPF_TEST=1 go test ./internal/probe -run 'TestCPU|TestMapStats|TestRunQLatTaskIdentityKernel' -v
make guest-check
make guest-kernel-check
GUEST_OUTPUT=log/guest-new-run make m9-test
python3 hack/validate-guest-contract.py log/guest-new-run
```

单元测试的 HTTP server 和 Postman simulation 都不是 Kata 替代物。已实际执行本地 Postman 1.86.0 simulation：生产 relay 经真实 net/http 请求 stand-in，9 个用例及 9 次 member 请求，覆盖 200、429、503 JSON/500 HTML→502、错误 UID/nonce→409、超大响应→502、拒绝跟随重定向、超时→504。结果见 [测试输出](m9-20261105/simulation-test.txt)、[sanitized summary](m9-20261105/simulation-validation.json)；不提交 raw simulation events，没有账号绑定、workspace 上传或云发布。

本次 API 技能促使把失败行为固化进 OpenAPI/schema、真实 HTTP 测试和独立 dependency simulation；实际内核/网络/计量精度仍需下述 live evidence，不能由 mock 推断。

## Nov 6 最终 Guest 六窗

原始数据：[guest-final-20261106](guest-final-20261106/effects.json)。六个目标窗口均第一尝试接受，无失败候选窗口；warmup 另行保留。CPU 是整个稳定进程的 CPU%，runqlat 则按最后两列解释覆盖范围。

| 阶段/窗口 | CPU% | 等待次数 | P95 us | runqlat 线程覆盖 | partial |
| --- | ---: | ---: | ---: | --- | --- |
| baseline 1 | 100.60 | 1404 | 255 | 4/5 | true |
| baseline 2 | 100.58 | 1558 | 127 | 4/5 | true |
| contention 1 | 108.12 | 52371 | 2047 | 16/16 | false |
| contention 2 | 111.79 | 49282 | 2047 | 16/16 | false |
| recovery 1 | 101.07 | 1205 | 127 | 5/6 | true |
| recovery 2 | 100.58 | 1198 | 127 | 5/6 | true |

窗口 P95 的描述性均值为 191→2047→127 us；支持在此配置下看见竞争/恢复趋势，**不代表完整进程/总体的 P95、显著性或因果证明**。只有 worker 固定单核，Go runtime 其他线程可运行在其他 Guest CPU，因此进程 CPU 略高于 100% 并不矛盾。

Probe 容器为 500m /128MiB limit。`--inspect` 时 cgroup current=45,641,728 bytes（43.53MiB），创建以来 peak=95,842,304 bytes（91.40MiB），memory.events 的 max/oom/oom_kill 均 0；累计 nr_throttled=8、throttled_usec=599098，不能宣称无限预算保证或无调度干扰。此诊断会额外启动一次 inspection 进程，cgroup 包含它；`status` 是 inspection 自身而非 daemon 的 RSS。inspect 不加载 BPF，输出 `ready:false` 也不代表常驻 Probe 不健康。原始：[资源诊断](guest-final-20261106/probe-resources.json)。

直接 Probe 响应没有 Sandbox ID，Node relay 正确补入当前 ID；runc 同名 exec 正对照可见，Host API 未把 Guest 进程冒充为 Host 进程；Guest 删除后旧 Sandbox relay 返回 404。Agent UID/重启与 map/event 指标、Pod restart/OOM、原 DaemonSet spec 完整恢复均由 harness 显式检查，不以一条 200 代替验收。

## 内核与接口验证结果

- [最终 capability 三对照](guest-capability-final-20261106/exit.json)：默认内核 missing BTF、debug 非特权 EPERM、debug 特权成功；三者按预期的 Failed/Failed/Succeeded 验证，最终退出 0。
- [Guest 真内核测试](guest-kernel-20261106/kernel.json)：CPU retention（8192 capacity 保留 128/128，特意缩到 16 后仅保留 7/128）、batch、namespace alias、map stats、原有 scheduler identity lookup 均通过；没有跳过 opt-in 内核测试。kernel.json 是当时保存的纯文本测试输出，不是 JSON。
- [Host 真内核测试](m9-20261106/host-kernel.txt)：相同组在 WSL Host 通过。Guest/Host retention 的 runtime/wait4 名义比值分别约 1.0007/1.0008，仅是这个小 fixture 的检查，不是跨负载精度证明。
- [完整 Go 测试](m9-20261106/go-test-final.txt)、[vet](m9-20261106/go-vet.txt)、[最终 race](m9-20261106/go-race-final.txt) 均通过；正常 Go test 不隐式运行 root BPF 测试，后两组内核测试单独启用。
- [Guest 契约](m9-20261106/guest-contract-validation.json)：19 份真实窗口/直接响应及 1 份删除错误响应，通过 OpenAPI 3.1 / JSON Schema 2020-12、format 与覆盖率不变量校验。包含 warmup 的 partial/null，不只验证接受窗口。
- [M8 回归](kata-m9-regression-20261106/exit.json)：同一最终 Agent 镜像、原有非 debug handler，在两容器共享 VM、Host/Guest exec 边界、官方 Monitor、Host CPU/memory/IO/vCPU、400/429、删除清理上通过；[Host 契约](m9-20261106/host-contract-validation.json) 验证 9 份响应与 2 份错误。现有 M8 脚本的描述性窗口 P95 均值为 1023→32767→4095 us，恢复低于竞争但没有回到 baseline；不当 Guest 延迟或跨层同步结果。
- [runc 回归](m9-runc-20261106/exit.json)：Guest relay 默认关闭时合法查询返回 503；完整 integration（lifecycle、CPU hotspot、runqlat）及两轮各 20000 子进程通过，Agent 同一 UID、restart=0、kernel eventsLost=0，map source ready 且 refresh errors=0。采样时 CPU runtime map 为 7449/8192，Agent RSS 约 54.10MiB，仅是该观察点而非峰值/开销差。原 spec 已 canonical compare 恢复。

## 结束状态与下一步

测试 namespace/Pods、临时 port-forward 和实验锁均已清理；原 Agent DaemonSet spec 恢复，Node Ready。专用 Guest runtime、官方 debug kernel 与网络修复保留作为后续开发环境，备份路径见前文。最终只读校验记录在 [final-state.json](m9-20261106/final-state.json)，流程是 [check-state.sh](m9-20261106/check-state.sh)。

M9 到此结束。下一阶段先研究 Host/Guest 时钟偏移与不确定性、同步窗口和受控的 Guest/Host contention 干预，而不是直接给两个不同时间窗口贴“根因”标签；同时需设计独立 Probe off/on 对照。VSOCK、生产安全和多 VM 容量均未在本 MR 中实现。
