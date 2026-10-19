#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-kno-dev}"
KUBE_CONTEXT="${KUBE_CONTEXT:-${MINIKUBE_PROFILE}}"
IMAGE="${IMAGE:-kubenode-observer/kno-agent:dev}"

if ! minikube -p "${MINIKUBE_PROFILE}" status >/dev/null 2>&1; then
  MINIKUBE_PROFILE="${MINIKUBE_PROFILE}" bash hack/start-minikube.sh
fi

make image-local IMAGE="${IMAGE}"
minikube -p "${MINIKUBE_PROFILE}" image load --overwrite=true "${IMAGE}"
kubectl --context "${KUBE_CONTEXT}" apply -f deploy/minikube/kno-agent.yaml
kubectl --context "${KUBE_CONTEXT}" -n kube-system set image daemonset/kno-agent agent="${IMAGE}"
kubectl --context "${KUBE_CONTEXT}" -n kube-system rollout restart daemonset/kno-agent
kubectl --context "${KUBE_CONTEXT}" -n kube-system rollout status daemonset/kno-agent --timeout=120s

echo "KubeNode-Observer installed: context=${KUBE_CONTEXT}, image=${IMAGE}"
