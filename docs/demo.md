# v0.1.0 Demo 指南

## 前置条件

- Linux x86_64 或 WSL2 Linux，内核提供 BTF；
- cgroup v2 和 tracefs；
- Go 1.22+、Docker、kubectl、Minikube；
- clang/LLVM 仅在重新生成 eBPF object 时需要；
- Minikube 基础节点镜像已能从所在环境获得。

先运行环境检查：

```bash
make check
```

## 一条命令完成演示

```bash
make demo
```

流程包括：

1. 若 `kno-dev` 不存在则以 Docker driver、containerd、bridge CNI 启动；
2. 构建 `kno-agent` 和三个 workload 的本地静态镜像；
3. 使用 `minikube image load --overwrite=true` 注入镜像；
4. 部署 privileged DaemonSet 并等待 Ready；
5. 验证 exec 事件归属、CPU 热点三级下钻、runqlat 与 cgroup PSI 的同向变化；
6. 清理测试 workload，保留 Agent 供手工查询。

成功输出至少包含：

```text
smoke test passed: workload exec event observed
CPU hotspot test passed: kno-cpu-hog-local is hottest
run queue latency test passed: baseline ... contention ...
Integration suite passed
Demo completed successfully
```

## 手工查看

建立本地端口转发：

```bash
kubectl --context kno-dev -n kube-system port-forward service/kno-agent 18080:18080
```

另一个终端中：

```bash
./bin/kno health
./bin/kno top pods
curl http://127.0.0.1:18080/metrics
```

运行某个业务 Pod 后可以继续下钻：

```bash
./bin/kno topology -n default --pod demo
./bin/kno events -n default --pod demo --limit 20
./bin/kno top containers -n default -p demo
./bin/kno top processes -n default -p demo
./bin/kno trace runqlat -n default -p demo --duration 10s
```

## 分步运行与清理

```bash
make install-minikube
make integration
make stability           # 40,000 个短进程，约 2 分钟
make benchmark
make uninstall-minikube
```

卸载只删除 KubeNode-Observer 的 RBAC、DaemonSet、Service 和 ServiceAccount，不删除 Minikube 集群或其他 workload。

## 结果定位

可重复实验记录位于 `log/`：M5 runqlat 与 PSI 对照、M6 自身指标、一键 demo 流水线以及 Observer off/on 原始轮次均保留为独立记录。
