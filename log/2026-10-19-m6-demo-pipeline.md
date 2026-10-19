# M6 一键 Demo 与集成流水线记录

日期：2026-10-19

## 目标

验证一个新使用者只通过公开入口即可构建本地镜像、部署 Agent，并重复运行 lifecycle、CPU hotspot、runqlat 三条核心演示链路。所有项目镜像均使用无 registry 域名的短名称。

## 公开入口

```text
make install-minikube
make integration
make demo
make uninstall-minikube
```

`make demo` 会在集群不存在时启动 `kno-dev`，构建并通过 `minikube image load` 注入四个本地镜像，然后部署 DaemonSet 并运行整套集成测试。

## 完整 Demo 实测

在已有 `kno-dev` 环境直接运行 `make demo`，结果：

```text
DaemonSet rollout: success
lifecycle: workload exec event observed
CPU hotspot: kno-cpu-hog-local ranked first at 102.67%
runqlat baseline P95: 31 us
runqlat contention P95: 2,047 us
cgroup CPU pressure baseline: 74,031 us
cgroup CPU pressure contention: 6,028,029 us
```

contention 窗口记录 66,130 次 runnable wait，平均 `533.20 us`、P50 `1,023 us`、P99 `2,047 us`。所有临时 workload 均由各测试脚本的 trap 清理。

## 卸载/重装验证

随后执行 `make uninstall-minikube`，确认 ServiceAccount、ClusterRole、ClusterRoleBinding、DaemonSet 和 Service 全部删除，且查询 `daemonset/kno-agent` 返回不存在。再执行 `make install-minikube`，Agent 成功回到 Ready `1/1`。

## 阶段 Review

- 安装使用显式 kube context，不依赖当前 context；
- 同名 `:dev` 镜像通过强制 load 加 rollout restart，避免节点继续运行旧 image ID；
- 测试镜像只从本机构建并注入 Minikube，不触发 Docker Hub 拉取；
- integration 统一构建一次再运行三条测试，避免原先逐目标重复构建；
- 卸载只删除 `deploy/minikube/kno-agent.yaml` 中项目资源，不删除 Minikube 集群和用户 workload；
- Minikube 基础节点镜像仍属于环境前置条件，脚本无法替代公司或个人环境的基础镜像缓存。
