#!/usr/bin/env bash
# Finite, debug-off acceptance on one Docker-backed Minikube node. Not a CPU
# overhead benchmark: Pod creation and external memory sampling add load.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
NODE_CONTAINER="${NODE_CONTAINER:-${KUBE_CONTEXT}}"
AGENT_IMAGE="${AGENT_IMAGE:-kubenode-observer/kno-agent:cpu-accounting}"
SOAK_SECONDS="${SOAK_SECONDS:-1800}"
SOAK_OUTPUT="${SOAK_OUTPUT:-log/soak-$(date -u +%Y%m%dT%H%M%SZ)}"
namespace=kno-soak-test
[[ "${SOAK_SECONDS}" =~ ^[0-9]+$ ]] && (( SOAK_SECONDS >= 30 && SOAK_SECONDS <= 7200 ))
command -v jq >/dev/null
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
[[ "$(kube get nodes -o name | wc -l)" -eq 1 ]]
[[ "$(docker exec "${NODE_CONTAINER}" hostname)" == "$(kube get nodes -o jsonpath='{.items[0].metadata.name}')" ]]
[[ -z "$(kube get ns "${namespace}" --ignore-not-found -o name)" ]]
mkdir -- "${SOAK_OUTPUT}"
git rev-parse HEAD >"${SOAK_OUTPUT}/source-head.txt"
git diff HEAD -- bpf cmd internal hack >"${SOAK_OUTPUT}/source.patch"
sha256sum bpf/cpu.bpf.c internal/probe/cpu.go internal/cpu/collector.go hack/soak-test.sh >"${SOAK_OUTPUT}/source-sha256.txt"
jq -n --arg context "${KUBE_CONTEXT}" --arg node "${NODE_CONTAINER}" --arg image "${AGENT_IMAGE}" --argjson duration "${SOAK_SECONDS}" '{context:$context,nodeContainer:$node,agentImage:$image,durationSeconds:$duration,childrenPerPod:2000,pauseSeconds:10,eventLimit:32}' >"${SOAK_OUTPUT}/config.json"
kube -n kube-system create configmap kno-experiment-lock --from-literal=run="${SOAK_OUTPUT}"
original_saved=false
namespace_owned=false
forward_pid=""
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if [[ -n "${forward_pid}" ]]; then kill "${forward_pid}" 2>/dev/null || true; wait "${forward_pid}" 2>/dev/null || true; fi
  if [[ "${namespace_owned}" == true ]]; then
    if ! kube delete ns "${namespace}" --wait=true --timeout=90s; then result=1; fi
  fi
  if [[ "${original_saved}" == true ]]; then
    if ! kube apply -f "${SOAK_OUTPUT}/original-manifest.json"; then result=1; fi
    if ! kube -n kube-system rollout status ds/kno-agent --timeout=90s; then result=1; fi
    if ! kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${SOAK_OUTPUT}/restored-agent.json"; then result=1; fi
  fi
  if ! kube -n kube-system delete cm kno-experiment-lock; then result=1; fi
  jq -n --argjson exitCode "${result}" '{exitCode:$exitCode}' >"${SOAK_OUTPUT}/exit.json"
  exit "${result}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kube -n kube-system get ds kno-agent -o json >"${SOAK_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.status)' "${SOAK_OUTPUT}/original-agent.json" >"${SOAK_OUTPUT}/original-manifest.json"
# Do not accidentally exercise a profiling configuration.
jq -e 'all(.spec.template.spec.containers[0].args[]; startswith("--debug-listen")|not)' "${SOAK_OUTPUT}/original-agent.json" >/dev/null
original_saved=true
kube -n kube-system set image ds/kno-agent "agent=${AGENT_IMAGE}"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube create ns "${namespace}"
namespace_owned=true
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${SOAK_OUTPUT}/agent-before.json"
uid="$(jq -r '.items[0].metadata.uid' "${SOAK_OUTPUT}/agent-before.json")"
cid="$(jq -r '.items[0].status.containerStatuses[0].containerID|sub("^[^:]+://";"")' "${SOAK_OUTPUT}/agent-before.json")"
docker exec "${NODE_CONTAINER}" crictl --runtime-endpoint unix:///run/containerd/containerd.sock inspect "${cid}" >"${SOAK_OUTPUT}/cri.json"
node_pid="$(jq -r '.info.pid' "${SOAK_OUTPUT}/cri.json")"
[[ "${node_pid}" =~ ^[0-9]+$ ]]
cgroup_path="$(docker exec "${NODE_CONTAINER}" cat "/proc/${node_pid}/cgroup" | sed -n 's/^0:://p')"
[[ "${cgroup_path}" == /* && "${cgroup_path}" != *..* ]]
memory_dir="/sys/fs/cgroup${cgroup_path}"
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${SOAK_OUTPUT}/forward.txt" 2>&1 &
forward_pid=$!
endpoint=""
for _ in $(seq 1 100); do
  address="$(awk '/Forwarding from 127.0.0.1:/ {print $3; exit}' "${SOAK_OUTPUT}/forward.txt")"
  if [[ -n "${address}" ]]; then endpoint="http://${address}"; break; fi
  sleep 0.1
done
[[ -n "${endpoint}" ]]
for path in diagnostics debug/pprof/; do
  [[ "$(curl --connect-timeout 2 --max-time 5 -sS -o /dev/null -w '%{http_code}' "${endpoint}/${path}")" == 404 ]]
done
started=${SECONDS}
sample() {
  local tag="$1"
  kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${SOAK_OUTPUT}/${tag}-agent.json"
  jq -e --arg uid "${uid}" --arg cid "${cid}" '.items|length==1 and .[0].metadata.uid==$uid and (.[0].status.containerStatuses[0] | .ready and .restartCount==0 and (.containerID|endswith($cid)))' "${SOAK_OUTPUT}/${tag}-agent.json" >/dev/null
  curl --connect-timeout 2 --max-time 5 -fsS "${endpoint}/metrics" >"${SOAK_OUTPUT}/${tag}-metrics.txt"
  [[ "$(awk '$1=="kno_agent_events_lost_total" {print $2}' "${SOAK_OUTPUT}/${tag}-metrics.txt")" == 0 ]]
  docker exec "${NODE_CONTAINER}" sh -c 'cat "$1/memory.current"; if test -f "$1/memory.peak"; then cat "$1/memory.peak"; else echo null; fi; cat "$1/memory.max"; cat "$1/memory.events"' sh "${memory_dir}" >"${SOAK_OUTPUT}/${tag}-memory.txt"
  jq -Rn --arg at "$(date -u +%FT%TZ)" --argjson elapsed "$((SECONDS-started))" --rawfile raw "${SOAK_OUTPUT}/${tag}-memory.txt" '
    ($raw|split("\n")) as $x | {at:$at,elapsedSeconds:$elapsed,currentBytes:($x[0]|tonumber),peakBytes:($x[1]|fromjson),maxBytes:$x[2],events:($x[3:]|map(select(length>0)|split(" ")|{key:.[0],value:(.[1]|tonumber)})|from_entries)}' >"${SOAK_OUTPUT}/${tag}-memory.json"
  jq -e '.maxBytes=="134217728" and .events.oom==0 and .events.oom_kill==0 and .events.max==0' "${SOAK_OUTPUT}/${tag}-memory.json" >/dev/null
  jq -c . "${SOAK_OUTPUT}/${tag}-memory.json" >>"${SOAK_OUTPUT}/samples.jsonl"
}
sample before
round=0
while (( SECONDS-started < SOAK_SECONDS )); do
  round=$((round+1))
  tag="$(printf '%03d' "${round}")"
  pod="churn-${tag}"
  jq -n --arg ns "${namespace}" --arg pod "${pod}" '{apiVersion:"v1",kind:"Pod",metadata:{namespace:$ns,name:$pod},spec:{restartPolicy:"Never",containers:[{name:"workload",image:"kubenode-observer/exec-test:dev",imagePullPolicy:"Never",args:["--iterations=2000","--interval=0s","--child-exit-code=0"],resources:{requests:{cpu:"100m",memory:"8Mi"},limits:{cpu:"1",memory:"32Mi"}}}]}}' >"${SOAK_OUTPUT}/${tag}-manifest.json"
  kube apply -f "${SOAK_OUTPUT}/${tag}-manifest.json" >/dev/null
  kube -n "${namespace}" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/${pod}" --timeout=90s >/dev/null
  kube -n "${namespace}" get pod "${pod}" -o json >"${SOAK_OUTPUT}/${tag}-pod.json"
  pod_uid="$(jq -r '.metadata.uid' "${SOAK_OUTPUT}/${tag}-pod.json")"
  pod_cid="$(jq -r '.status.containerStatuses[0].containerID|sub("^[^:]+://";"")' "${SOAK_OUTPUT}/${tag}-pod.json")"
  matched=false
  for attempt in $(seq 1 20); do
    events="${SOAK_OUTPUT}/${tag}-events-${attempt}.json"
    curl --connect-timeout 2 --max-time 5 -fsS "${endpoint}/api/v1/events?namespace=${namespace}&pod=${pod}&limit=32" >"${events}"
    if jq -e --arg uid "${pod_uid}" --arg cid "${pod_cid}" '(.events|length>0) and all(.events[]; .workload.podUid==$uid and .workload.containerId==$cid)' "${events}" >/dev/null; then matched=true; break; fi
    sleep 1
  done
  [[ "${matched}" == true ]]
  kube -n "${namespace}" delete pod "${pod}" --wait=true >/dev/null
  sample "${tag}"
  echo "soak round=${round} elapsed=$((SECONDS-started))s: 2000 children, identities matched, Agent healthy"
  sleep 10
done
sample after
cleared=false
for attempt in $(seq 1 30); do
  curl --connect-timeout 2 --max-time 5 -fsS "${endpoint}/api/v1/topology?namespace=${namespace}" >"${SOAK_OUTPUT}/topology-after-${attempt}.json"
  if jq -e '.snapshot!=null and (.snapshot.containers|length)==0' "${SOAK_OUTPUT}/topology-after-${attempt}.json" >/dev/null; then cleared=true; break; fi
  sleep 1
done
[[ "${cleared}" == true ]]
echo "Soak passed: ${round} Pods, $((round*2000)) children, $((SECONDS-started)) seconds; restoring original Agent."
