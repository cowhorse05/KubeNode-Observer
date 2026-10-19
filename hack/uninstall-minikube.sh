#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-kno-dev}"
KUBE_CONTEXT="${KUBE_CONTEXT:-${MINIKUBE_PROFILE}}"

kubectl --context "${KUBE_CONTEXT}" delete -f deploy/minikube/kno-agent.yaml --ignore-not-found
echo "KubeNode-Observer removed from context ${KUBE_CONTEXT}"
