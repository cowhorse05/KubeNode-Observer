# KubeNode-Observer

Kubernetes 节点出现热点时，节点级指标只能说明“哪里变慢了”，很难直接回答“哪个 Pod、容器和进程导致了问题”。
KubeNode-Observer 是一个以 Go、eBPF 和 Kubernetes 为核心的节点观测项目，目标是建立 Node → Pod → Container → Process 的关联，并为后续的 microVM/Kata Containers 隔离开销研究提供可复现实验基础。

当前已经完成进程生命周期观测闭环：节点 DaemonSet 捕获 exec/exit 事件，通过 cgroup ID 关联 Kubernetes Workload，并由 API 和 CLI 提供可过滤的事件查询。

## 当前能力

- 基于 `cilium/ebpf` 加载 CO-RE eBPF 程序；
- 捕获进程 exec/exit 事件及 PID、TGID、PPID、comm、cgroup ID、退出码；
- 通过 Kubernetes Informer、CRI、procfs 和 cgroup v2 构建并交叉校验 Pod → Container → Process 拓扑快照；
- 使用当前拓扑与短期 tombstone 为事件补全 namespace、Pod、Container 和镜像身份；
- 以特权 DaemonSet 运行，并最小化挂载节点 tracefs；
- 提供 `/healthz`、`/api/v1/topology`、`/api/v1/events`、`kno health`、`kno topology` 和 `kno events`；
- 统计 ring buffer 预留失败造成的事件丢失，并通过 `eventsLost` 暴露；
- 提供不依赖远端镜像仓库的本地端到端 smoke test。

## 本地开发

要求 Linux/WSL2 内核启用 BTF、cgroup v2 和 tracefs，并安装 Go、clang/LLVM、Docker、kubectl 与 Minikube。

```bash
make check
make generate
make test
make build
```

启动开发集群并部署：

```bash
./hack/start-minikube.sh
make image-local
minikube -p kno-dev image load --overwrite=true kubenode-observer/kno-agent:dev
kubectl --context kno-dev apply -f deploy/minikube/kno-agent.yaml
kubectl --context kno-dev -n kube-system rollout status daemonset/kno-agent
```

运行完全本地的端到端验证：

```bash
make smoke
```

查看 Agent 原始日志：

```bash
kubectl --context kno-dev -n kube-system logs -l app.kubernetes.io/name=kno-agent -f
```

访问健康接口：

```bash
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080
./bin/kno health
```

查询指定 Pod 的容器、cgroup 和进程拓扑：

```bash
./bin/kno topology --namespace kube-system --pod coredns-7d764666f9-lfstc
```

查询已归属的进程生命周期事件：

```bash
./bin/kno events --namespace default --pod kno-exec-test-local --type exec --limit 20
./bin/kno events --namespace default --pod kno-exec-test-local --type exit --limit 20
```

`exitCode` 是面向用户的退出码，`rawExitCode` 保留内核 tracepoint 提供的 wait status；因信号退出时还会返回 `exitSignal`。

拓扑中的 `cgroupId` 是 cgroup v2 目录 inode，可直接与 `bpf_get_current_cgroup_id()` 返回值关联。CRI 负责提供容器 PID 和 Kubernetes 标签，Informer 负责验证 Pod UID、Phase 与 QoS，`/host/proc` 负责确认实际进程归属。

## 内网镜像约定

项目中的基础镜像和示例镜像使用不带 registry 域名的短名称，例如 `library/golang:1.26-alpine` 和 `iclaw/busybox:1.37.0-musl`，由公司环境的容器运行时镜像仓库配置完成解析。标准 Minikube 会把短名称补全为 `docker.io/...`；在无法访问 Docker Hub 的个人环境中，优先使用 `Dockerfile.local`、`minikube image load` 和 `make smoke`，它们不需要拉取运行时镜像。

完整的研究问题、里程碑与验收指标见 [docs/plan.md](docs/plan.md)。

