#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
ROUNDS="${CHURN_ROUNDS:-2}"
TEST_POD="kno-process-churn-local"
LOCAL_PORT="${KNO_STABILITY_PORT:-18089}"
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

agent_restart_count() {
  kubectl --context "${KUBE_CONTEXT}" -n kube-system get pods \
    -l app.kubernetes.io/name=kno-agent \
    -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}'
}

restart_before="$(agent_restart_count)"
kubectl --context "${KUBE_CONTEXT}" -n kube-system port-forward service/kno-agent "${LOCAL_PORT}:18080" \
  >"${TEST_TMP_DIR}/port-forward.log" 2>&1 &
PORT_FORWARD_PID=$!
for _ in $(seq 1 20); do
  if curl -fsS "http://127.0.0.1:${LOCAL_PORT}/metrics" >"${TEST_TMP_DIR}/metrics-before" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if [[ ! -s "${TEST_TMP_DIR}/metrics-before" ]]; then
  echo "agent metrics endpoint did not become ready" >&2
  exit 1
fi
events_lost_before="$(awk '$1 == "kno_agent_events_lost_total" { print $2 }' "${TEST_TMP_DIR}/metrics-before")"

for round in $(seq 1 "${ROUNDS}"); do
  kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found >/dev/null
  kubectl --context "${KUBE_CONTEXT}" apply -f examples/exec-test/churn-local.yaml >/dev/null
  for _ in $(seq 1 300); do
    phase="$(kubectl --context "${KUBE_CONTEXT}" get pod "${TEST_POD}" -o jsonpath='{.status.phase}')"
    if [[ "${phase}" == "Succeeded" ]]; then
      break
    fi
    if [[ "${phase}" == "Failed" ]]; then
      echo "process churn Pod failed" >&2
      exit 1
    fi
    sleep 1
  done
  if [[ "${phase}" != "Succeeded" ]]; then
    echo "process churn Pod did not complete within 300 seconds" >&2
    exit 1
  fi
  echo "process churn round ${round}: 20000 child processes completed"
done

curl -fsS "http://127.0.0.1:${LOCAL_PORT}/metrics" >"${TEST_TMP_DIR}/metrics-after"

restart_after="$(agent_restart_count)"
events_lost_after="$(awk '$1 == "kno_agent_events_lost_total" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
rss="$(awk '$1 == "kno_agent_resident_memory_bytes" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
heap="$(awk '$1 == "kno_agent_go_heap_alloc_bytes" { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
cpu_entries="$(awk '$1 ~ /map="cpu_runtime"/ && $1 ~ /^kno_agent_bpf_map_entries/ { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
cpu_capacity="$(awk '$1 ~ /map="cpu_runtime"/ && $1 ~ /^kno_agent_bpf_map_max_entries/ { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
start_entries="$(awk '$1 ~ /map="cpu_task_start"/ && $1 ~ /^kno_agent_bpf_map_entries/ { print $2 }' "${TEST_TMP_DIR}/metrics-after")"
start_capacity="$(awk '$1 ~ /map="cpu_task_start"/ && $1 ~ /^kno_agent_bpf_map_max_entries/ { print $2 }' "${TEST_TMP_DIR}/metrics-after")"

if [[ "${restart_after}" != "${restart_before}" ]]; then
  echo "stability test failed: agent restart count changed ${restart_before} -> ${restart_after}" >&2
  exit 1
fi
if [[ "${events_lost_after}" != "${events_lost_before}" ]]; then
  echo "stability test failed: events lost changed ${events_lost_before} -> ${events_lost_after}" >&2
  exit 1
fi
if [[ "${cpu_capacity}" != "8192" || "${start_capacity}" != "1" ]]; then
  echo "stability test failed: unexpected CPU map capacity" >&2
  exit 1
fi

echo "stability test passed: restarts=${restart_after}, eventsLost=${events_lost_after}"
echo "CPU maps: runtime=${cpu_entries}/${cpu_capacity}, task_start=${start_entries}/${start_capacity}"
echo "agent memory: RSS=${rss} bytes, Go heap=${heap} bytes"
