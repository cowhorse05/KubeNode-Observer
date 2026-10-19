#!/usr/bin/env bash
set -euo pipefail

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-kno-dev}"
LOCAL_PORT="${KNO_TEST_PORT:-18082}"
TRACE_DURATION="${TRACE_DURATION:-5s}"
BASELINE_POD="kno-runqlat-baseline"
CONTENTION_POD="kno-runqlat-contention"
TEST_TMP_DIR="$(mktemp -d)"
PORT_FORWARD_PID=""

cleanup() {
  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
  kubectl --context "${KUBE_CONTEXT}" delete pod "${BASELINE_POD}" "${CONTENTION_POD}" --ignore-not-found --wait=false >/dev/null
  rm -rf -- "${TEST_TMP_DIR}"
}
trap cleanup EXIT

kubectl --context "${KUBE_CONTEXT}" -n kube-system port-forward service/kno-agent "${LOCAL_PORT}:18080" \
  >"${TEST_TMP_DIR}/port-forward.log" 2>&1 &
PORT_FORWARD_PID=$!
for _ in $(seq 1 20); do
  if curl -fsS "http://127.0.0.1:${LOCAL_PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

run_case() {
  local manifest="$1"
  local pod="$2"
  local output_file="$3"
  local pressure_file="$4"
  kubectl --context "${KUBE_CONTEXT}" delete pod "${BASELINE_POD}" "${CONTENTION_POD}" --ignore-not-found
  kubectl --context "${KUBE_CONTEXT}" apply -f "${manifest}"
  kubectl --context "${KUBE_CONTEXT}" wait --for=condition=Ready "pod/${pod}" --timeout=60s
  sleep 5
  local topology cgroup_path pressure_before pressure_after
  topology="$(./bin/kno --endpoint "http://127.0.0.1:${LOCAL_PORT}" topology --namespace default --pod "${pod}")"
  cgroup_path="$(sed -n 's/.*"cgroupPath": "\([^"]*\)".*/\1/p' <<<"${topology}" | head -1)"
  if [[ -z "${cgroup_path}" ]]; then
    echo "run queue latency test failed: cgroup path not found for ${pod}" >&2
    exit 1
  fi
  pressure_before="$(minikube -p "${MINIKUBE_PROFILE}" ssh -- cat "/sys/fs/cgroup${cgroup_path}/cpu.pressure")"
  ./bin/kno --endpoint "http://127.0.0.1:${LOCAL_PORT}" trace runqlat \
    --namespace default --pod "${pod}" --duration "${TRACE_DURATION}" >"${output_file}"
  pressure_after="$(minikube -p "${MINIKUBE_PROFILE}" ssh -- cat "/sys/fs/cgroup${cgroup_path}/cpu.pressure")"
  local before_total after_total
  before_total="$(awk '/^some / { for (i = 1; i <= NF; i++) { split($i, value, "="); if (value[1] == "total") print value[2] } }' <<<"${pressure_before}" | tr -d '\r')"
  after_total="$(awk '/^some / { for (i = 1; i <= NF; i++) { split($i, value, "="); if (value[1] == "total") print value[2] } }' <<<"${pressure_after}" | tr -d '\r')"
  echo "$((after_total - before_total))" >"${pressure_file}"
}

run_case examples/cpu-contention/baseline-local.yaml "${BASELINE_POD}" "${TEST_TMP_DIR}/baseline.json" "${TEST_TMP_DIR}/baseline-pressure"
run_case examples/cpu-contention/contention-local.yaml "${CONTENTION_POD}" "${TEST_TMP_DIR}/contention.json" "${TEST_TMP_DIR}/contention-pressure"

baseline_p95="$(sed -n 's/.*"p95Us": \([0-9]*\).*/\1/p' "${TEST_TMP_DIR}/baseline.json" | head -1)"
contention_p95="$(sed -n 's/.*"p95Us": \([0-9]*\).*/\1/p' "${TEST_TMP_DIR}/contention.json" | head -1)"
baseline_count="$(sed -n 's/.*"count": \([0-9]*\).*/\1/p' "${TEST_TMP_DIR}/baseline.json" | head -1)"
contention_count="$(sed -n 's/.*"count": \([0-9]*\).*/\1/p' "${TEST_TMP_DIR}/contention.json" | head -1)"
baseline_pressure="$(<"${TEST_TMP_DIR}/baseline-pressure")"
contention_pressure="$(<"${TEST_TMP_DIR}/contention-pressure")"

if [[ -z "${baseline_p95}" || -z "${contention_p95}" || -z "${baseline_count}" || -z "${contention_count}" ]]; then
  echo "run queue latency test failed: incomplete response" >&2
  exit 1
fi
if ! awk -v baseline="${baseline_p95}" -v contention="${contention_p95}" \
  -v baseline_count="${baseline_count}" -v contention_count="${contention_count}" \
  'BEGIN { exit !(baseline_count > 5 && contention_count > 5 && contention >= baseline * 2 && contention >= 100) }'; then
  echo "run queue latency test failed: baseline p95=${baseline_p95}us, contention p95=${contention_p95}us" >&2
  echo "baseline:" >&2
  cat "${TEST_TMP_DIR}/baseline.json" >&2
  echo "contention:" >&2
  cat "${TEST_TMP_DIR}/contention.json" >&2
  exit 1
fi
if ! awk -v baseline="${baseline_pressure}" -v contention="${contention_pressure}" \
  'BEGIN { exit !(contention >= 1000 && (baseline == 0 || contention >= baseline * 2)) }'; then
  echo "CPU pressure comparison failed: baseline=${baseline_pressure}us, contention=${contention_pressure}us" >&2
  exit 1
fi

echo "run queue latency test passed: baseline p95=${baseline_p95}us, contention p95=${contention_p95}us"
echo "cgroup CPU pressure: baseline=${baseline_pressure}us, contention=${contention_pressure}us"
echo "contention result:"
cat "${TEST_TMP_DIR}/contention.json"
