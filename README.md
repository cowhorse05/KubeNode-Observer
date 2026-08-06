# KubeNode-Observer

Kubernetes 节点出现热点时，Prometheus 只能看到节点指标，无法快速定位具体容器和进程。本项目尝试实现一个节点侧观测 Agent，为 Pod、Container 和 Process 建立关联关系，并探索结合 eBPF 进行异常诊断

