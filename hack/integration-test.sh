#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-kno-dev}"
KUBE_CONTEXT="${KUBE_CONTEXT:-${MINIKUBE_PROFILE}}"
WORKLOAD_IMAGE="${WORKLOAD_IMAGE:-kubenode-observer/exec-test:dev}"
CPU_HOG_IMAGE="${CPU_HOG_IMAGE:-kubenode-observer/cpu-hog:dev}"
CONTENTION_IMAGE="${CONTENTION_IMAGE:-kubenode-observer/cpu-contention:dev}"

make build workload-image cpu-hog-image contention-image \
  WORKLOAD_IMAGE="${WORKLOAD_IMAGE}" \
  CPU_HOG_IMAGE="${CPU_HOG_IMAGE}" \
  CONTENTION_IMAGE="${CONTENTION_IMAGE}"

for image in "${WORKLOAD_IMAGE}" "${CPU_HOG_IMAGE}" "${CONTENTION_IMAGE}"; do
  minikube -p "${MINIKUBE_PROFILE}" image load --overwrite=true "${image}"
done

KUBE_CONTEXT="${KUBE_CONTEXT}" bash hack/smoke-test.sh
KUBE_CONTEXT="${KUBE_CONTEXT}" bash hack/cpu-hotspot-test.sh
KUBE_CONTEXT="${KUBE_CONTEXT}" MINIKUBE_PROFILE="${MINIKUBE_PROFILE}" bash hack/runqlat-test.sh

echo "Integration suite passed: lifecycle, CPU hotspot, run queue latency"
