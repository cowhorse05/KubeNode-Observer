# M8：Host/VMM 资源视图

M7 回答“这个 Kata Pod 对应哪个 Host VM”；M8 进一步回答“VM 的哪些 Host 线程消耗 CPU、vCPU 有没有在 Host 上排队”。不进入 Guest，不把双容器共享 VM 的资源重复算给两个容器。

## 使用

先按 [Kata 指南](kata.md)安装运行时，在节点上部署本分支构建的 Agent，然后 port-forward：

```bash
./bin/kno --endpoint http://127.0.0.1:18080 top workloads -n default --duration=5s --limit=20
curl 'http://127.0.0.1:18080/api/v1/host/resources?namespace=default&kind=kata&duration=5s'
```

CLI 输出 JSON，便于保留原始实验数据。完整契约见 [OpenAPI](../api/host-resources.openapi.yaml)。现有 `top pods/containers/processes` 和 `/api/v1/cpu` 不改变：它们没有偷偷混入 VMM 或 Guest 的 CPU。

## 数据含义

| 字段 | Kata sandbox 行 | runc container 行 |
| --- | --- | --- |
| `cpu` | 稳定 VMM 线程的 eBPF runtime 差值之和，`accounting=vmm-threads` | 独立 cgroup 累计量差值，`accounting=cgroup` |
| `memory` | 窗口末点 `/proc/PID/status` 的 VmRSS，`source=proc-status-rss` | 窗口末点 `memory.current`，`source=cgroup-memory.current` |
| `io` | `/proc/PID/io` 起止差值：read/write bytes、rchar/wchar | 未实现，`null` |
| `threads` | Node TID、内核 TID/TGID、starttime、cgroup、角色及 CPU/runqlat | 空数组 |
| `vcpuRunqlat` | vCPU 线程的窗口直方图相加后计算分位数 | `null` |

`domain=host` 是整份响应的边界。Kata 以 sandbox 为单位，两个 Guest 容器只产生一条 VM 行；runc 以 container 为单位。CPU 100% 表示一个逻辑 CPU 的 runtime，不按节点核数归一化，多线程可超过 100%。每个 CPU 值附带实际 `windowSeconds`；`durationSeconds` 是整个请求测量耗时，不应当拿来重新计算所有字段。

两种 memory **不等价**：RSS 不是 Guest 已用内存，也不是 VM 配置内存；cgroup current 还包括其记账范围内的缓存等。IO 是 Host QEMU 进程记账，不是 Guest 块设备吞吐；全零差值可以是合法观测。不要直接求和或作“隔离开销”结论。

`vcpuRunqlat` 是 Host 调度等待：仅说明 QEMU vCPU 线程 runnable 后等待被 Host 调度的时间，不能说明 Guest 进程等待了多久。分位数是微秒 log2 bucket 的上界；聚合的是 bucket，不是各线程 P95 的平均数。`count=0` 只表示窗口里没有完成并记录的等待。

## 关联链和新增身份字段

1. 复用 M7 的 `Pod UID → Sandbox ID → verified VMM PID`，校验 PID starttime、exe、cgroup 路径与 inode。
2. 从该 VMM 的 `/proc/PID/task` 枚举线程。每条线程单独读取 cgroup：vCPU 可能与 QEMU 主线程位于不同 cgroup，不能直接套用父进程 cgroup。
3. eBPF key 保留内核全局 PID/TGID；CPU value 额外记录 **Agent 所在 PID namespace** 的 PID/TGID，作为 procfs join 的别名。Agent 必须保持现有 `hostPID: true`，并挂载同一 Node procfs。任意不同命名空间的 `--host-proc` 不在支持范围。
4. 通过“namespace TGID + namespace TID + 线程 cgroup”得到唯一 CPU map key，再用其全局 key 匹配现有 runqlat map。禁止将 namespace PID 数字直接当成全局 key。
5. 起止端重复验证进程和线程身份。缺失、重置、迁移、重命名或生成变化均显式说明，不以 0 代替。

因此线程同时有 `tid`（Node procfs 可用）和 `kernelTid/kernelTgid`（eBPF key）；`startTimeTicks` 用于识别 PID/TID 复用，`cgroupId` 用于防止跨资源组误连。新增的两个 namespace 字段不改变旧 CPU API 中 PID 的含义。

角色识别限定为已验证的 `qemu-system-x86_64` 进程：`CPU N/KVM` → `vcpu`，主线程 → `vmm-main`，其余 → `other`。响应 `roleSource=qemu-thread-name` 明确标识这是 [QEMU 的命名约定](https://raw.githubusercontent.com/qemu/qemu/v11.0.1/accel/kvm/kvm-accel-ops.c)，不是权威的 QMP 枚举；没有连接或占用 Kata 管理的 QMP socket。改名的 vCPU 不会被猜测补全，其他 hypervisor 尚未验证。

## 缺失与边界

- 字段 `null` 表示没有可靠结果，不表示资源消耗为零。`issues` 给原因，`partial=true` 表示该行至少一项观测不完整。合法但不支持的 runc IO/threads 不单独产生 partial。
- 完整稳定线程集合且每线程 CPU 都有效，才返回 VMM CPU 总量；个别线程有效时仍保留线程值。无法观测的休眠线程也不能凭空假设为 0。
- vCPU 直方图必须有完整起止计数器；LRU 淘汰、别名缺失、计数器回退等返回不可用。线程新建/消失使 VM CPU 不完整；当前其他线程的有效值仍可查看。
- procfs、CPU 和 runqlat map 快照均非跨源原子事务，线程扫描也有竞态，不能当逐事件严格对齐的证明。线程在两个端点之间短暂迁移后恢复、或尚未记入 switch-out 的运行片段，可能无法发现。
- 请求只读、按需测量，不新增后台扫描器/Guest Probe/探针。CPU value 从 24 增至 32 bytes，8192 项逻辑 value 增量 64 KiB；namespace helper 仅在新 key 插入时调用。其他 map 容量、Agent 默认配额不变，不能据此断言无开销。

接口只允许一个本接口的测量窗口，第二个返回 429（不排队）；这不是与旧 runqlat/debug 接口共享的全局实验锁，做实验应独占节点采集任务。duration 100ms..30s，limit 1..100，筛选最多 256 行、每 VM 最多 512 线程；limit 是排序后的输出条数，不绕过测量上限。未知/重复参数返回 400，非 GET 返回 405；拓扑未就绪/超过 30s/刷新失败、超界或 map 扫描失败返回 503。进程级观测失败保留 partial 行。

API 无 TLS/鉴权，仅供受信任开发节点使用，不能直接暴露公网。

## 可重复实验

```bash
make m8-test
```

前提：Kata 已安装，Docker-driver Minikube 单节点，节点可运行 QEMU/KVM，Node 有 taskset/python3/jq。镜像均由本地静态二进制构建并 image load，不携带仓库域名、不访问 Docker Hub。

脚本复用 M7 的独占锁、临时 namespace、Agent 替换/恢复和 Host/Guest 正负对照。只把临时测试 VM 线程和临时竞争 Pod 线程固定到同一个允许 CPU，不改全局 affinity/sysctl：baseline 两窗 → 同核竞争两窗 → 删除竞争者后的 recovery 两窗，每窗 5 秒。保留原始直方图、procfs/schedstat 外部参照、镜像/源码指纹、失败尝试、删除后清理与 HTTP 错误语义。

外部 procfs 参照窗口包住 HTTP 调用，边界并不完全相同，只能检查 runtime 趋势，不能当精确同窗误差；`sched_schedstats=0` 时不能使用 schedstat waitNs 作等待精度基准。实验中六个窗口只是机制验证，不是统计显著性、生产容量或 runc/Kata 性能优劣的结论。正式跨层研究留到 M9/M10。

保存的 HTTP/CLI 响应可以离线对照 OpenAPI 的 JSON Schema 校验（需 Python PyYAML、jsonschema）：`python3 hack/validate-host-contract.py log/<本次目录>`。不依赖外部 Postman 工作区，也不上传节点信息。
