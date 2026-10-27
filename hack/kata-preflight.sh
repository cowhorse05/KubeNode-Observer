#!/usr/bin/env bash
# Docker-driver, single-node development Minikube only. Never changes runtimes.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
command -v python3 >/dev/null
command -v jq >/dev/null
node="$(kubectl --context "${KUBE_CONTEXT}" get nodes -o json | jq -er 'if (.items|length)==1 then .items[0].metadata.name else error("requires one node") end')"
if [[ "$(docker exec "${MINIKUBE_PROFILE}" hostname)" != "${node}" ]]; then
  echo "profile Docker container does not match selected Kubernetes node" >&2; exit 1
fi
kubectl --context "${KUBE_CONTEXT}" wait --for=condition=Ready "node/${node}" --timeout=30s
docker exec "${MINIKUBE_PROFILE}" uname -a
docker exec "${MINIKUBE_PROFILE}" ls -l /dev/kvm /dev/vhost-vsock /dev/vhost-net
docker exec -i "${MINIKUBE_PROFILE}" python3 - < hack/kata-kvm-check.py
docker exec "${MINIKUBE_PROFILE}" stat -fc %T /sys/fs/cgroup
docker exec "${MINIKUBE_PROFILE}" df -B1 /dev/shm
shm_bytes="$(docker exec "${MINIKUBE_PROFILE}" df -B1 --output=size /dev/shm | tail -1 | tr -d ' ')"
if (( shm_bytes < 1073741824 )); then
  echo 'WARNING: Docker /dev/shm is smaller than 1 GiB; virtio-fs Guest RAM can exhaust it even when KVM checks pass.' >&2
fi
docker exec "${MINIKUBE_PROFILE}" containerd --version
docker exec "${MINIKUBE_PROFILE}" crictl info | jq '{defaultRuntime:.config.containerd.defaultRuntimeName,runtimes:(.config.containerd.runtimes|keys)}'
