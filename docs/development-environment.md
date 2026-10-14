# 开发与验证环境

本文记录 M0 可行性验证使用的环境，便于后续在个人电脑和公司集群复现实验。

## 已验证环境

验证日期：2026-10-13

| 组件 | 版本或状态 |
| --- | --- |
| Host | Windows + WSL2 Ubuntu 24.04.1 LTS |
| Kernel | `6.6.114.1-microsoft-standard-WSL2` |
| Go | `1.22.2` |
| clang | `18.1.3` |
| Minikube | `1.38.1` |
| Kubernetes | `1.35.1` |
| containerd | `2.2.1` |
| cgroup | v2 |
| Kernel BTF | `/sys/kernel/btf/vmlinux` 可用 |
| tracefs | `/sys/kernel/tracing` 可用 |

## 验证结果

- `kno-agent` 可在 WSL2 宿主机直接加载 eBPF 程序；
- `kno-agent` DaemonSet 可在 Minikube 节点加载 `sched_process_exec` tracepoint；
- 本地测试 Pod 的 `workload` 进程事件能够经 ring buffer 送达 Go 用户态；
- `/healthz` 能报告 eBPF 加载状态、运行时间与事件总数；
- Agent 与 smoke workload 都可通过本地静态二进制构建，不依赖 Docker Hub 运行时镜像。
- CRI、Kubernetes Informer、procfs 与 cgroup v2 的拓扑结果已完成三方校验；测试 cgroup inode 与 eBPF 事件中的 cgroup ID 一致。

Minikube 容器需要显式挂载节点的 `/sys/kernel/tracing`。仅设置 `privileged: true` 不会把宿主机 tracefs 自动带入容器的 mount namespace。

## 镜像解析差异

公司环境通过容器运行时配置解析不带 registry 域名的镜像短名称；项目清单遵循这一约定。标准 Minikube 未配置相同 mirror 时会把短名称解析到 Docker Hub，因此本地验证使用 `minikube image load` 注入项目镜像。
