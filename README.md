# KubeNode-Observer

Kubernetes 节点出现热点时，节点级指标只能说明“哪里变慢了”，很难直接回答“哪个 Pod、容器和进程导致了问题”。KubeNode-Observer 是一个以 Go、eBPF 和 Kubernetes 为核心的节点观测项目，目标是建立 Node → Pod → Container → Process 的关联，并为后续的 microVM/Kata Containers 隔离开销研究提供可复现实验基础。

当前完成了第一个可运行闭环：节点 DaemonSet 加载 `sched_process_exec` tracepoint，通过 ring buffer 将进程执行事件送入 Go，并暴露健康与事件计数接口。

## 当前能力

- 基于 `cilium/ebpf` 加载 CO-RE eBPF 程序；
- 捕获进程执行事件及 PID、TGID、comm、cgroup ID；
- 以特权 DaemonSet 运行，并最小化挂载节点 tracefs；
- 提供 `/healthz` 和 `kno health`；
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

查看事件：

```bash
kubectl --context kno-dev -n kube-system logs -l app.kubernetes.io/name=kno-agent -f
```

访问健康接口：

```bash
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080
./bin/kno health
```

## 内网镜像约定

项目中的基础镜像和示例镜像使用不带 registry 域名的短名称，例如 `library/golang:1.26-alpine` 和 `iclaw/busybox:1.37.0-musl`，由公司环境的容器运行时镜像仓库配置完成解析。标准 Minikube 会把短名称补全为 `docker.io/...`；在无法访问 Docker Hub 的个人环境中，优先使用 `Dockerfile.local`、`minikube image load` 和 `make smoke`，它们不需要拉取运行时镜像。

完整的研究问题、里程碑与验收指标见 [docs/plan.md](docs/plan.md)。

