# M9：Kata Guest 进程窗口

M9 把同一组 CPU/runqlat eBPF 程序加载到 **Guest 内核**，不让 Host 通过 VMM PID 猜 Guest 进程。它是可信研究环境的最小 sidecar，不是生产部署方案，也不是完整 VM 监控器。

## 路径与边界

```text
kno guest resources --sandbox <完整 ID>
  → Node Agent：当前 CRI Sandbox + VMM generation + Informer Pod UID/IP
  → 固定 Pod IP:18081：Guest Probe
  → Guest sched_switch/wakeup → 原有 CPU/runqlat maps
  → shared Pod procfs 校验进程/线程 generation → 窗口差值
  → Node 再校验身份，添加 sandboxId/namespace/podName
```

Probe 没有 Kubernetes 客户端行为、ServiceAccount token、Host 挂载或 Sandbox 自报权限；Pod UID 来自 Downward API。只有 Node relay 能加入当前权威 Sandbox ID。请求有新 nonce，拒绝错误 UID、重放 nonce、预填 Sandbox 身份、Pod IP/VMM generation 变化。**这只是关联校验，不是密码学证明**；恶意 Guest 仍可伪造测量。

Probe privileged 的范围在 Kata Guest 内，但仍需信任 Guest 内的其他工作负载。HTTP 没有 TLS/鉴权，sidecar 服务直接可达；固定端口、字面量 Pod IP、显式 annotation、禁用代理/重定向只收窄路由，不形成完整安全隔离。不要面向不可信租户或公网部署。

`shareProcessNamespace: true` 只让 procfs 看见共享 **Pod PID namespace** 的进程，不是 Guest init PID namespace 的全部任务。BPF 附着 Guest 全局调度事件，但只返回在 procfs 起点可见、经验证的进程。窗口中新建进程不补入结果；`totalItems` 是起点匹配行数，不是整个 Pod/VM 进程或 CPU 总量。

## 先验证内核，不自研内核

已验证 Kata 4.3.1 Go/QEMU 11.0.1。官方包的默认 Guest kernel 缺 BTF/BPF_EVENTS；同包 `vmlinux-debug.container` 提供所需配置。两者 `uname` 都是 `6.18.56`，**不能只凭版本字符串判断可用**。依据是 kernel/config hash、`CONFIG_DEBUG_INFO_BTF=y` / `CONFIG_BPF_EVENTS=y` 和真实加载测试。

参考官方 [4.3.1 kernel 构建说明](https://github.com/kata-containers/kata-containers/blob/4.3.1/tools/packaging/kernel/README.md) 与 [privileged 说明](https://github.com/kata-containers/kata-containers/blob/4.3.1/docs/how-to/privileged.md)。本阶段没有改 BPF C、生成对象或 map 容量；只增加已有 `task_identity` map 的只读 lookup。

在已经按 [M7 指南](kata.md) 安装官方包、配置 `/dev/shm` 后：

```bash
make guest-install                       # 一次性、拒绝覆盖已有配置
make guest-check                         # 默认内核失败、非特权失败、debug 特权成功
make guest-kernel-check                  # scratch 测试镜像内运行真实内核测试
GUEST_OUTPUT=log/guest-my-run make m9-test # 本地构建/导入、测试、恢复 Agent
```

`guest-install` 只增加 `kata-qemu-guest` handler / `kno-kata-qemu-guest` RuntimeClass，并把原配置备份为 `/etc/containerd/config.toml.kno-before-guest`；保留 runc 默认和 M7/M8 `kata-qemu`。安装会重启开发节点 containerd，要求没有 Ready Kata Sandbox、没有其他实验锁。硬编码的配置路径只针对实测版本，不是通用安装器。

所有镜像引用不带 registry 域名，生产与测试镜像都是本地静态二进制 + `FROM scratch`，通过 `minikube image load` 加载，Pod 使用 `imagePullPolicy: Never`，不需要 Docker Hub。内核测试额外需要本机 x86_64 C assembler/linker；极小汇编 fixture 仅提供测试用 true/sleep，不进入生产镜像。

个人 PC 首先确认 Linux x86_64、KVM/nested virtualization、WSL BTF、cgroup v2、Kata 官方包和足够内存。这里验证的是 `kno-dev` Minikube，**没有在公司 tdk 集群部署 Kata/privileged Probe**；不要把单节点脚本直接用于共享集群。

实验前检查 Node 内 Kubernetes Service VIP 可达且 Informer 有 Node UID。手动 `docker start` 可能丢失 Minikube 写入的 `control-plane.minikube.internal` hosts 项，导致 kube-proxy 初始化失败；本次修复与备份见 [报告](../log/2026-11-06-kata-guest.md)。优先正常使用 Minikube 生命周期，不把 hosts 修复脚本当通用步骤重复执行。

## 手动查询

Agent 默认 `--guest-probe-port=0`，禁用 relay。手工使用时给 Agent 增加 `--guest-probe-port=18081`，Pod 需有：

```yaml
metadata:
  annotations:
    observer.kubenode.io/guest-probe: enabled
spec:
  runtimeClassName: kno-kata-qemu-guest
  shareProcessNamespace: true
  automountServiceAccountToken: false
```

完整 Probe 与 workload 示例见 `examples/guest-probe/pods.yaml`。不要把 Probe 改成 hostNetwork，也不要传调用者指定的 URL。

```bash
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080
./bin/kno topology --namespace kno-guest-test
./bin/kno guest resources --sandbox <64位小写hexID> --comm guest-burn --duration=5s --limit=20
```

接口契约：[Guest OpenAPI 3.1](../api/guest-resources.openapi.yaml)。默认 5s，范围 100ms..10s；limit 1..100；comm 精确匹配，最多 15 bytes。每个 Probe/relay 只允许一个并发窗口（429、Retry-After 1）；最多 256 可见进程、每进程 512 线程，响应不超过 2MiB。超过扫描范围失败，不悄悄截断测量；limit 仅在测量后排序截取。

## 字段为什么存在

| 字段 | 解决的问题 |
| --- | --- |
| `domain=guest` | 不与 `/api/v1/host/resources` 的 Host 资源混算 |
| `podUid` / relay 添加的 `sandboxId` | 区分同名重建 Pod 与新一代 VM；不相信 Guest 自报 Sandbox |
| `bootId` / `instanceId` / `requestId` | 分别区分内核启动、Probe 进程实例、本次请求；不代表认证 |
| `namespacePid` / `startTimeTicks` | procfs 里的 PID 及 generation，防止 PID 重用 |
| `pid` / `cgroupId` | 校验后的 **Guest 全局 TGID / Guest cgroup**，不能关联 Host cgroup inode |
| `cpu.windowSeconds` | 实际 CPU 起止快照间隔，CPU% = runtimeNs / 该间隔 ×100 |
| `durationSeconds` / `startedAt` | 整次扫描的实际耗时 / Guest 墙钟开始时间，不是 Host 同步时间线 |
| `runqlatCoverage` | histogram 纳入的线程数 / 窗口起点线程数，揭示部分覆盖 |
| `partial` / `issues` / `null` | 不能完整测量的原因，不用 0 假装测到 |

Go workload 通过 Kata exec 进入运行 cgroup 时，可能留下同一 namespace PID 的历史 CPU cgroup key。不能选第一个/最大的计数；用原有 runqlat 的 `task_identity`（最近 switch-out 的 cgroup/TGID）筛选，再要求两端 global PID/TGID/cgroup、proc starttime、comm、线程集合及累计计数一致。该 map 不是瞬时当前 cgroup 的原子证明；两端之间发生又恢复的短迁移仍可能漏检。

CPU 必须覆盖完整、稳定线程集合，否则 CPU 和 runqlat 均为 null。runqlat 只加总两端 histogram 均存在且单调的稳定线程；有一部分可测时保留子集分布，同时 `partial=true`。**4/5 线程覆盖不能叫完整进程 P95**，即使缺失线程看起来是 idle。缺失端点不是零；两端确实存在但差值为 0 才是该覆盖子集的 observed zero。

P50/P95/P99 是微秒 log2 bucket 上界，不是精确分位数；多个窗口的 P95 平均值也不是 pooled P95。BPF maps/procfs/CPU 与 histogram 读取不是跨层原子快照，LRU 淘汰仍有限制。Host 的 `processVisibility=guest-unavailable` 保持原语义：**Host 不能直接看 Guest 进程**，不是否认独立 Guest 接口可用。

## 验收与下一阶段

`m9-test` 运行 1 worker → 8 workers → 1 worker（worker 固定 Guest 同一逻辑 CPU），各两个 5s 窗口。每窗口最多三次、仅按身份/计数完整性选择，所有尝试都保存；趋势单独输出，不因 P95 没涨到阈值而把整个实验判失败。Host 查询在 Guest 窗口后单独执行，仅证明两种视图同一 Sandbox，不宣称时间对齐。

测试同时验证直接 Probe 不带 Sandbox ID、relay 关联、runc exec 正对照、Host 不冒充 Guest、删除后 404、Probe 无重启/OOM、Host map/丢事件指标，以及原 DaemonSet spec 恢复。原始响应可离线校验：

资源诊断 `--inspect` 不加载 BPF，因此其 `ready:false` 不是常驻 daemon 的健康结果；它读取的 cgroup 内存包含这次额外的 inspection 进程，`status` 也仅是自身进程。不要把它误写成 daemon RSS 或纯空闲成本。

```bash
python3 hack/validate-guest-contract.py log/guest-my-run
```

需安装 PyYAML/jsonschema。单元测试使用真实 loopback HTTP server 覆盖错误/身份变化；另有已实际运行的 Postman 本地 simulation，复用生产 relay 调用 mock dependency，见 `postman/simulations/guest-relay/guest-relay.context.md`。simulation 不证明 BPF 精度或 Pod 网络，真实 Kata 实验单独保存。没有创建账号、上传 workspace 或发布 mock。

M9 不做 Host/Guest 时钟偏移估计、同步窗口、跨层归因、VSOCK、生产安全加固或 Probe 开销因果比较。这些问题应在 M10 中用受控实验研究，不能仅凭本阶段两层指标同时可查就宣称解决。
