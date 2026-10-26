#!/usr/bin/env bash
# Destructive only to the explicitly selected single-node development Agent.
# Keep this script in Linux rather than embedding shell variables in PS/WSL args.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
AGENT_IMAGE="${AGENT_IMAGE:-kubenode-observer/kno-agent:metrics-cache}"
VERIFY_OUTPUT="${VERIFY_OUTPUT:-log/metrics-validation-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -- "${VERIFY_OUTPUT}"
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node_count="$(kube get nodes -o name | wc -l)"
if [[ "${node_count}" -ne 1 ]]; then echo "requires a single-node development cluster" >&2; exit 1; fi
existing="$(kube -n default get pods kno-exec-test-local kno-cpu-hog-local kno-runqlat-baseline kno-runqlat-contention kno-process-churn-local --ignore-not-found -o name)"
if [[ -n "${existing}" ]]; then echo "test Pod already exists; refusing to replace it" >&2; exit 1; fi
kube -n kube-system create configmap kno-experiment-lock --from-literal=run="${VERIFY_OUTPUT}"
original_image=""
forward_pid=""
cleanup() {
  task_result=$?
  trap - EXIT
  if [[ -n "${forward_pid}" ]]; then
    kill "${forward_pid}" 2>/dev/null || true
    wait "${forward_pid}" 2>/dev/null || true
  fi
  if [[ -n "${original_image}" ]]; then
    if ! kube -n kube-system set image ds/kno-agent "agent=${original_image}"; then task_result=1; fi
    if ! kube -n kube-system rollout status ds/kno-agent --timeout=90s; then task_result=1; fi
    if ! kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${VERIFY_OUTPUT}/restored-agent.json"; then task_result=1; fi
  fi
  if ! kube -n kube-system delete configmap kno-experiment-lock; then task_result=1; fi
  exit "${task_result}"
}
trap cleanup EXIT
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${VERIFY_OUTPUT}/original-agent.json"
original_image="$(kube -n kube-system get ds kno-agent -o jsonpath='{.spec.template.spec.containers[0].image}')"
if [[ -z "${original_image}" ]]; then echo "cannot determine original image" >&2; exit 1; fi
kube -n kube-system set image ds/kno-agent "agent=${AGENT_IMAGE}"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${VERIFY_OUTPUT}/before-agent.json"
before_uid="$(kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o jsonpath='{.items[0].metadata.uid}')"
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${VERIFY_OUTPUT}/port-forward.txt" 2>&1 &
forward_pid=$!
endpoint=""
for _ in $(seq 1 100); do
  address="$(awk '/Forwarding from 127.0.0.1:/ {print $3; exit}' "${VERIFY_OUTPUT}/port-forward.txt")"
  if [[ -n "${address}" ]]; then endpoint="http://${address}"; break; fi
  sleep 0.1
done
if [[ -z "${endpoint}" ]]; then echo "port-forward did not start" >&2; exit 1; fi

cache_ready() {
  awk '$1 ~ /^kno_agent_bpf_map_stats_ready\{/ {n++; ready += $2}
       $1 == "kno_agent_metrics_collection_errors" {errors=$2; found=1}
       END {exit !(n==3 && ready==3 && found && errors==0)}' "$1"
}
curl --connect-timeout 2 --max-time 5 -fsS "${endpoint}/metrics" >"${VERIFY_OUTPUT}/metrics-before.txt"
cache_ready "${VERIFY_OUTPUT}/metrics-before.txt"
for path in diagnostics debug/pprof/; do
  status="$(curl --connect-timeout 2 --max-time 5 -sS -o "${VERIFY_OUTPUT}/public-${path//\//_}.txt" -w '%{http_code}' "${endpoint}/${path}")"
  if [[ "${status}" != "404" ]]; then echo "diagnostics exposed on normal API: ${path}" >&2; exit 1; fi
done

KUBE_CONTEXT="${KUBE_CONTEXT}" MINIKUBE_PROFILE="${KUBE_CONTEXT}" make integration 2>&1 | tee "${VERIFY_OUTPUT}/integration.txt"
KUBE_CONTEXT="${KUBE_CONTEXT}" CHURN_ROUNDS=2 make stability 2>&1 | tee "${VERIFY_OUTPUT}/stability.txt"
# Keep the immediate sample; retries must not erase transient failures.
ready=false
for attempt in $(seq 1 65); do
  file="${VERIFY_OUTPUT}/metrics-after-${attempt}.txt"
  curl --connect-timeout 2 --max-time 5 -fsS "${endpoint}/metrics" >"${file}"
  if cache_ready "${file}"; then ready=true; break; fi
  sleep 0.5
done
if [[ "${ready}" != "true" ]]; then echo "map cache did not recover within 65 attempts" >&2; exit 1; fi
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${VERIFY_OUTPUT}/after-agent.json"
after_uid="$(kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o jsonpath='{.items[0].metadata.uid}')"
restarts="$(kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}')"
if [[ "${before_uid}" != "${after_uid}" || "${restarts}" != "0" ]]; then echo "Agent replaced/restarted" >&2; exit 1; fi
echo "Metrics cache verification passed; original Agent will now be restored."
