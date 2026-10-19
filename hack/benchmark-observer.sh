#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
ROUNDS="${BENCHMARK_ROUNDS:-5}"
TEST_POD="kno-observer-benchmark"
TEST_TMP_DIR="$(mktemp -d)"
PORT_FORWARD_PID=""

cleanup() {
  if [[ -n "${PORT_FORWARD_PID}" ]]; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
  fi
  kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found --wait=false >/dev/null
  kubectl --context "${KUBE_CONTEXT}" apply -f deploy/minikube/kno-agent.yaml >/dev/null
  rm -rf -- "${TEST_TMP_DIR}"
}
trap cleanup EXIT

run_case() {
  local mode="$1"
  local round="$2"
  kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found >/dev/null
  kubectl --context "${KUBE_CONTEXT}" apply -f examples/observer-benchmark/pod-local.yaml >/dev/null
  for _ in $(seq 1 120); do
    phase="$(kubectl --context "${KUBE_CONTEXT}" get pod "${TEST_POD}" -o jsonpath='{.status.phase}')"
    if [[ "${phase}" == "Succeeded" ]]; then
      break
    fi
    if [[ "${phase}" == "Failed" ]]; then
      kubectl --context "${KUBE_CONTEXT}" logs "${TEST_POD}" >&2 || true
      echo "benchmark Pod failed" >&2
      exit 1
    fi
    sleep 1
  done
  if [[ "${phase}" != "Succeeded" ]]; then
    echo "benchmark Pod did not complete within 120 seconds (phase=${phase})" >&2
    exit 1
  fi
  local output throughput p99
  output="$(kubectl --context "${KUBE_CONTEXT}" logs "${TEST_POD}")"
  throughput="$(sed -n 's/.*"throughputOpsPerSecond":\([0-9.]*\).*/\1/p' <<<"${output}")"
  p99="$(sed -n 's/.*"p99Us":\([0-9]*\).*/\1/p' <<<"${output}")"
  if [[ -z "${throughput}" || -z "${p99}" ]]; then
    echo "invalid benchmark response: ${output}" >&2
    exit 1
  fi
  printf '%s %s %s\n' "${mode}" "${throughput}" "${p99}" >>"${TEST_TMP_DIR}/results"
  echo "${mode} round ${round}: throughput=${throughput} ops/s, p99=${p99} us"
}

median() {
  sort -n | awk '{ values[NR]=$1 } END { if (NR % 2) print values[(NR+1)/2]; else print (values[NR/2]+values[NR/2+1])/2 }'
}

kubectl --context "${KUBE_CONTEXT}" -n kube-system delete daemonset kno-agent --ignore-not-found >/dev/null
for _ in $(seq 1 60); do
  if [[ -z "$(kubectl --context "${KUBE_CONTEXT}" -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o name)" ]]; then
    break
  fi
  sleep 1
done
for round in $(seq 1 "${ROUNDS}"); do
  run_case off "${round}"
done

kubectl --context "${KUBE_CONTEXT}" apply -f deploy/minikube/kno-agent.yaml >/dev/null
kubectl --context "${KUBE_CONTEXT}" -n kube-system rollout status daemonset/kno-agent --timeout=120s >/dev/null

kubectl --context "${KUBE_CONTEXT}" -n kube-system port-forward service/kno-agent 18084:18080 \
  >"${TEST_TMP_DIR}/port-forward.log" 2>&1 &
PORT_FORWARD_PID=$!
for _ in $(seq 1 20); do
  if curl -fsS http://127.0.0.1:18084/metrics >"${TEST_TMP_DIR}/metrics-before" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if [[ ! -s "${TEST_TMP_DIR}/metrics-before" ]]; then
  echo "agent metrics endpoint did not become ready" >&2
  exit 1
fi
agent_started="$(date +%s.%N)"
for round in $(seq 1 "${ROUNDS}"); do
  run_case on "${round}"
done
agent_finished="$(date +%s.%N)"
curl -fsS http://127.0.0.1:18084/metrics >"${TEST_TMP_DIR}/metrics-after"

off_throughput="$(awk '$1 == "off" { print $2 }' "${TEST_TMP_DIR}/results" | median)"
on_throughput="$(awk '$1 == "on" { print $2 }' "${TEST_TMP_DIR}/results" | median)"
off_p99="$(awk '$1 == "off" { print $3 }' "${TEST_TMP_DIR}/results" | median)"
on_p99="$(awk '$1 == "on" { print $3 }' "${TEST_TMP_DIR}/results" | median)"
cpu_before="$(awk '$1 == "kno_agent_cpu_seconds_total" { print $2 }' "${TEST_TMP_DIR}/metrics-before")"
cpu_after="$(awk '$1 == "kno_agent_cpu_seconds_total" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
rss="$(awk '$1 == "kno_agent_resident_memory_bytes" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
memlock="$(awk '$1 ~ /^kno_agent_bpf_map_memlock_bytes{/ { total += $2 } END { print total }' "${TEST_TMP_DIR}/metrics-after")"
events_lost="$(awk '$1 == "kno_agent_events_lost_total" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
restart_count="$(kubectl --context "${KUBE_CONTEXT}" -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}')"

awk -v off_t="${off_throughput}" -v on_t="${on_throughput}" \
  -v off_p="${off_p99}" -v on_p="${on_p99}" \
  -v cpu_before="${cpu_before}" -v cpu_after="${cpu_after}" \
  -v started="${agent_started}" -v finished="${agent_finished}" \
  -v rss="${rss}" -v memlock="${memlock}" \
  -v events_lost="${events_lost}" -v restart_count="${restart_count}" 'BEGIN {
    printf "median throughput: observer off=%.2f, on=%.2f ops/s, delta=%.2f%%\n", off_t, on_t, (on_t/off_t-1)*100
    printf "median p99: observer off=%.0f, on=%.0f us, delta=%.2f%%\n", off_p, on_p, (on_p/off_p-1)*100
    printf "agent average CPU during on rounds: %.3f%%\n", (cpu_after-cpu_before)/(finished-started)*100
    printf "agent RSS after benchmark: %.0f bytes\n", rss
    printf "BPF map memlock total: %.0f bytes\n", memlock
    printf "events lost: %.0f, agent restarts: %.0f\n", events_lost, restart_count
  }'
