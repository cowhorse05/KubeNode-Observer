#!/usr/bin/env bash
set -euo pipefail

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
TEST_POD="kno-cpu-hog-local"
LOCAL_PORT="${KNO_TEST_PORT:-18081}"
MIN_CPU_PERCENT="${MIN_CPU_PERCENT:-70}"
MAX_CPU_PERCENT="${MAX_CPU_PERCENT:-110}"
TEST_TMP_DIR="$(mktemp -d)"
PORT_FORWARD_PID=""

cleanup() {
  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
  kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found --wait=false >/dev/null
  rm -rf -- "${TEST_TMP_DIR}"
}
trap cleanup EXIT

kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found
kubectl --context "${KUBE_CONTEXT}" apply -f examples/cpu-hog/pod-local.yaml
kubectl --context "${KUBE_CONTEXT}" wait --for=condition=Ready "pod/${TEST_POD}" --timeout=60s

kubectl --context "${KUBE_CONTEXT}" -n kube-system port-forward service/kno-agent "${LOCAL_PORT}:18080" \
  >"${TEST_TMP_DIR}/port-forward.log" 2>&1 &
PORT_FORWARD_PID=$!

for _ in $(seq 1 20); do
  if curl -fsS "http://127.0.0.1:${LOCAL_PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

output=""
for _ in $(seq 1 20); do
  if output="$(./bin/kno --endpoint "http://127.0.0.1:${LOCAL_PORT}" top pods --namespace default --limit 1 2>/dev/null)" && \
    grep -q '"podName": "kno-cpu-hog-local"' <<<"${output}"; then
    cpu_percent="$(sed -n 's/.*"cpuPercent": \([0-9.]*\).*/\1/p' <<<"${output}" | head -1)"
    if [[ -n "${cpu_percent}" ]] && awk -v value="${cpu_percent}" -v minimum="${MIN_CPU_PERCENT}" -v maximum="${MAX_CPU_PERCENT}" \
      'BEGIN { exit !(value >= minimum && value <= maximum) }'; then
      container_output="$(./bin/kno --endpoint "http://127.0.0.1:${LOCAL_PORT}" top containers --namespace default --pod "${TEST_POD}" --limit 1)"
      process_output="$(./bin/kno --endpoint "http://127.0.0.1:${LOCAL_PORT}" top processes --namespace default --pod "${TEST_POD}" --limit 1)"
      grep -q '"containerName": "workload"' <<<"${container_output}"
      grep -q '"command": "workload"' <<<"${process_output}"
      echo "CPU hotspot test passed: kno-cpu-hog-local is hottest at ${cpu_percent}% CPU"
      echo "${output}"
      exit 0
    fi
  fi
  sleep 1
done

echo "CPU hotspot test failed: test Pod was not ranked first" >&2
echo "${output:-no CPU sample}" >&2
exit 1
