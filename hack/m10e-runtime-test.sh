#!/usr/bin/env bash
# M10e runc/Kata AB/BA runtime comparison. Captures are gates; effect sizes are not.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
M10E_OUTPUT="${M10E_OUTPUT:-log/m10e-runtime-$(date -u +%Y%m%dT%H%M%SZ)}"
M10E_IMAGE="${M10E_IMAGE:-kubenode-observer/observer-benchmark:m10e}"
M10E_AGENT_IMAGE="${M10E_AGENT_IMAGE:-kubenode-observer/kno-agent:m10e}"
M10E_KATA_RUNTIME_CLASS="${M10E_KATA_RUNTIME_CLASS:-kno-kata-qemu}"
M10E_PAIRS="${M10E_PAIRS:-4}"
M10E_DURATION_SECONDS="${M10E_DURATION_SECONDS:-10}"
M10E_WARMUP_SECONDS="${M10E_WARMUP_SECONDS:-3}"
M10E_WORKERS="${M10E_WORKERS:-8}"
M10E_WORK_ITERATIONS="${M10E_WORK_ITERATIONS:-64}"
M10E_NAMESPACE="kno-m10e-test"
M10E_POD="kno-runtime-benchmark"

[[ "${M10E_PAIRS}" =~ ^([2468]|10)$ ]]
[[ "${M10E_DURATION_SECONDS}" =~ ^([5-9]|[1-5][0-9]|60)$ ]]
[[ "${M10E_WARMUP_SECONDS}" =~ ^([1-9]|1[0-9]|20)$ ]]
[[ "${M10E_WORKERS}" =~ ^([1-9]|[1-5][0-9]|6[0-4])$ ]]
[[ "${M10E_WORK_ITERATIONS}" =~ ^[1-9][0-9]*$ ]]

kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
get() { curl --fail-with-body -sS --connect-timeout 2 --max-time 60 "${agent_endpoint}$1"; }

namespace_owned=false
lock_owned=false
restore_agent=false
agent_forward=""
current_stem=""

stop_forward() {
  local variable="$1"
  local pid="${!variable:-}"
  if [[ -n "${pid}" ]]; then
    kill "${pid}" 2>/dev/null || true
    wait "${pid}" 2>/dev/null || true
    printf -v "${variable}" ''
  fi
}

cleanup() {
  code=$?
  trap - EXIT
  stop_forward agent_forward
  if [[ "${code}" != 0 && "${namespace_owned}" == true ]]; then
    kube -n "${M10E_NAMESPACE}" get pods -o json >"${M10E_OUTPUT}/failure-pods.json" 2>/dev/null || true
    kube -n "${M10E_NAMESPACE}" describe pods >"${M10E_OUTPUT}/failure-describe.txt" 2>/dev/null || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${M10E_OUTPUT}/failure-agent.txt" 2>/dev/null || true
  fi
  if [[ "${namespace_owned}" == true ]]; then
    kube delete namespace "${M10E_NAMESPACE}" --wait=true --timeout=180s || code=1
  fi
  if [[ "${restore_agent}" == true ]]; then
    kube apply -f "${M10E_OUTPUT}/original-agent-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=120s || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${M10E_OUTPUT}/restored-agent-spec.json" || code=1
    cmp "${M10E_OUTPUT}/original-agent-spec.json" "${M10E_OUTPUT}/restored-agent-spec.json" || code=1
  fi
  if [[ "${lock_owned}" == true ]]; then
    kube -n kube-system delete configmap kno-experiment-lock || code=1
  fi
  printf '{"exitCode":%s}\n' "${code}" >"${M10E_OUTPUT}/exit.json"
  exit "${code}"
}
mkdir -- "${M10E_OUTPUT}"
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
[[ -z "$(kube get namespace "${M10E_NAMESPACE}" --ignore-not-found -o name)" ]]
kube get runtimeclass "${M10E_KATA_RUNTIME_CLASS}" >/dev/null
KUBE_CONTEXT="${KUBE_CONTEXT}" MINIKUBE_PROFILE="${MINIKUBE_PROFILE}" bash hack/kata-preflight.sh >"${M10E_OUTPUT}/preflight.txt"
kube -n kube-system create configmap kno-experiment-lock --from-literal=run="${M10E_OUTPUT}"
lock_owned=true

jq -n \
  --arg createdAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg kubeContext "${KUBE_CONTEXT}" \
  --arg minikubeProfile "${MINIKUBE_PROFILE}" \
  --arg namespace "${M10E_NAMESPACE}" \
  --arg podName "${M10E_POD}" \
  --arg image "${M10E_IMAGE}" \
  --arg agentImage "${M10E_AGENT_IMAGE}" \
  --arg kataRuntimeClass "${M10E_KATA_RUNTIME_CLASS}" \
  --argjson pairs "${M10E_PAIRS}" \
  --argjson durationSeconds "${M10E_DURATION_SECONDS}" \
  --argjson warmupSeconds "${M10E_WARMUP_SECONDS}" \
  --argjson workers "${M10E_WORKERS}" \
  --argjson workIterations "${M10E_WORK_ITERATIONS}" \
  '{createdAt:$createdAt,kubeContext:$kubeContext,minikubeProfile:$minikubeProfile,
    namespace:$namespace,podName:$podName,image:$image,agentImage:$agentImage,kataRuntimeClass:$kataRuntimeClass,
    pairs:$pairs,durationSeconds:$durationSeconds,warmupSeconds:$warmupSeconds,
    workers:$workers,workIterations:$workIterations,
    resources:{requests:{cpu:"1",memory:"32Mi"},limits:{cpu:"1",memory:"128Mi"}}}' \
  >"${M10E_OUTPUT}/config.json"

git rev-parse HEAD >"${M10E_OUTPUT}/source-head.txt"
git -c core.autocrlf=true status --short --untracked-files=all \
  | grep -v '^?? ref-project/' \
  | grep -v '^?? log/m10e-runtime-' \
  >"${M10E_OUTPUT}/source-status.txt" || true
git -c core.autocrlf=true diff --binary -- . ':(exclude)ref-project/**' >"${M10E_OUTPUT}/source.patch"
sha256sum Makefile hack/m10e-runtime-test.sh hack/summarize-m10e.py hack/m10e_test.py \
  examples/observer-benchmark/{main.go,server.go,Dockerfile.local} \
  internal/host/*.go internal/topology/*.go >"${M10E_OUTPUT}/source-sha256.txt"
docker image inspect "${M10E_IMAGE}" "${M10E_AGENT_IMAGE}" >"${M10E_OUTPUT}/images.json"
kube get nodes -o json >"${M10E_OUTPUT}/nodes.json"
jq -e '.items|length==1' "${M10E_OUTPUT}/nodes.json" >/dev/null
kube version -o json >"${M10E_OUTPUT}/kubernetes-version.json"
kube get runtimeclass "${M10E_KATA_RUNTIME_CLASS}" -o json >"${M10E_OUTPUT}/runtimeclass.json"
kube -n kube-system get ds kno-agent -o json >"${M10E_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' \
  "${M10E_OUTPUT}/original-agent.json" >"${M10E_OUTPUT}/original-agent-manifest.json"
jq -S .spec "${M10E_OUTPUT}/original-agent.json" >"${M10E_OUTPUT}/original-agent-spec.json"
jq --arg image "${M10E_AGENT_IMAGE}" \
  '.spec.template.spec.containers[0].image=$image | .spec.template.spec.containers[0].imagePullPolicy="Never"' \
  "${M10E_OUTPUT}/original-agent-manifest.json" >"${M10E_OUTPUT}/candidate-agent-manifest.json"
restore_agent=true
kube apply -f "${M10E_OUTPUT}/candidate-agent-manifest.json"
kube -n kube-system rollout status ds/kno-agent --timeout=120s
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${M10E_OUTPUT}/agent-before.json"

kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${M10E_OUTPUT}/agent-forward.txt" 2>&1 &
agent_forward=$!
agent_endpoint=""
for _ in $(seq 1 100); do
  address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${M10E_OUTPUT}/agent-forward.txt")"
  if [[ -n "${address}" ]]; then agent_endpoint="http://${address}"; break; fi
  sleep .1
done
[[ -n "${agent_endpoint}" ]]
get /healthz >"${M10E_OUTPUT}/health-before.json"
get /metrics >"${M10E_OUTPUT}/metrics-before.txt"

informer_ready=false
for _ in $(seq 1 30); do
  get /api/v1/topology >"${M10E_OUTPUT}/informer.json"
  if jq -e '(.snapshot.nodeUid//"")!=""' "${M10E_OUTPUT}/informer.json" >/dev/null; then informer_ready=true; break; fi
  sleep 1
done
[[ "${informer_ready}" == true ]]

kube create namespace "${M10E_NAMESPACE}"
namespace_owned=true

write_manifest() {
  local runtime="$1" path="$2" runtime_class=""
  if [[ "${runtime}" == kata ]]; then runtime_class="${M10E_KATA_RUNTIME_CLASS}"; fi
  jq -n \
    --arg namespace "${M10E_NAMESPACE}" --arg name "${M10E_POD}" \
    --arg image "${M10E_IMAGE}" --arg runtimeClassName "${runtime_class}" \
    --argjson workIterations "${M10E_WORK_ITERATIONS}" \
    '{apiVersion:"v1",kind:"Pod",metadata:{namespace:$namespace,name:$name},
      spec:({restartPolicy:"Never",automountServiceAccountToken:false,
        containers:[{name:"benchmark",image:$image,imagePullPolicy:"Never",
          args:["--listen=:8080",("--work-iterations="+($workIterations|tostring))],
          readinessProbe:{httpGet:{path:"/healthz",port:8080},periodSeconds:1},
          resources:{requests:{cpu:"1",memory:"32Mi"},limits:{cpu:"1",memory:"128Mi"}}}]}
        + if $runtimeClassName=="" then {} else {runtimeClassName:$runtimeClassName} end)}' >"${path}"
}

capture_topology() {
  local runtime="$1" uid="$2" destination="$3" ready=false candidate
  for attempt in $(seq 1 45); do
    candidate="${M10E_OUTPUT}/${current_stem}-topology-attempt-${attempt}.json"
    get "/api/v1/topology?namespace=${M10E_NAMESPACE}" >"${candidate}"
    if [[ "${runtime}" == runc ]]; then
      if jq -e --arg uid "${uid}" '([(.snapshot.containers//[])[]|select(.podUid==$uid)]|length)==1 and ([(.snapshot.sandboxes//[])[]|select(.podUid==$uid)]|length)==0' "${candidate}" >/dev/null; then ready=true; break; fi
    else
      if jq -e --arg uid "${uid}" '([(.snapshot.containers//[])[]|select(.podUid==$uid and .processVisibility=="guest-unavailable")]|length)==1 and ([(.snapshot.sandboxes//[])[]|select(.podUid==$uid and .hypervisor.pid>0)]|length)==1' "${candidate}" >/dev/null; then ready=true; break; fi
    fi
    sleep 1
  done
  [[ "${ready}" == true ]]
  cp "${candidate}" "${destination}"
}

measure_case() {
  local pair="$1" position="$2" runtime="$3"
  current_stem="$(printf 'pair-%02d-%s-%s' "${pair}" "${position}" "${runtime}")"
  local manifest="${M10E_OUTPUT}/${current_stem}-manifest.json"
  local runtime_class_json=null kind=runc timeout=90s
  if [[ "${runtime}" == kata ]]; then
    runtime_class_json="\"${M10E_KATA_RUNTIME_CLASS}\""
    kind=kata
    timeout=180s
  fi
  jq -n --argjson pair "${pair}" --argjson position "${position}" --arg runtime "${runtime}" \
    --argjson runtimeClassName "${runtime_class_json}" \
    '{pair:$pair,position:$position,runtime:$runtime,runtimeClassName:$runtimeClassName}' \
    >"${M10E_OUTPUT}/${current_stem}-condition.json"
  write_manifest "${runtime}" "${manifest}"
  kube apply -f "${manifest}"
  kube -n "${M10E_NAMESPACE}" wait --for=condition=Ready "pod/${M10E_POD}" --timeout="${timeout}"
  kube -n "${M10E_NAMESPACE}" get pod "${M10E_POD}" -o json >"${M10E_OUTPUT}/${current_stem}-pod.json"
  jq -e '.status.phase=="Running" and (.status.containerStatuses|length)==1 and .status.containerStatuses[0].ready and .status.containerStatuses[0].restartCount==0 and (.status.containerStatuses[0].imageID//"")!=""' \
    "${M10E_OUTPUT}/${current_stem}-pod.json" >/dev/null
  uid="$(jq -r .metadata.uid "${M10E_OUTPUT}/${current_stem}-pod.json")"
  capture_topology "${runtime}" "${uid}" "${M10E_OUTPUT}/${current_stem}-topology.json"

  pod_ip="$(jq -r .status.podIP "${M10E_OUTPUT}/${current_stem}-pod.json")"
  [[ "${pod_ip}" =~ ^[0-9a-fA-F:.]+$ ]]
  node curl -fsS --connect-timeout 2 --max-time "$((M10E_DURATION_SECONDS+M10E_WARMUP_SECONDS+30))" \
    "http://${pod_ip}:8080/run?mode=steady&duration=${M10E_DURATION_SECONDS}s&warmup=${M10E_WARMUP_SECONDS}s&workers=${M10E_WORKERS}" \
    >"${M10E_OUTPUT}/${current_stem}-workload.json"
  jq -e --argjson workers "${M10E_WORKERS}" --argjson iterations "${M10E_WORK_ITERATIONS}" \
    '.mode=="steady" and .workers==$workers and .workIterations==$iterations and .operations>0 and .throughputOpsPerSecond>0 and .p99Us>0' \
    "${M10E_OUTPUT}/${current_stem}-workload.json" >/dev/null
  get "/api/v1/host/resources?namespace=${M10E_NAMESPACE}&pod=${M10E_POD}&kind=${kind}&duration=1s" \
    >"${M10E_OUTPUT}/${current_stem}-host.json"
  expected_kind=runc-container
  if [[ "${runtime}" == kata ]]; then expected_kind=kata-sandbox; fi
  jq -e --arg uid "${uid}" --arg kind "${expected_kind}" \
    '([.items[]|select(.podUid==$uid and .kind==$kind and .cpu!=null)]|length)==1' \
    "${M10E_OUTPUT}/${current_stem}-host.json" >/dev/null

  kube -n "${M10E_NAMESPACE}" delete pod "${M10E_POD}" --wait=true --timeout=180s
  gone=false
  for _ in $(seq 1 45); do
    get "/api/v1/topology?namespace=${M10E_NAMESPACE}" >"${M10E_OUTPUT}/${current_stem}-topology-deleted.json"
    if jq -e --arg uid "${uid}" '([(.snapshot.containers//[])[]|select(.podUid==$uid)]|length)==0 and ([(.snapshot.sandboxes//[])[]|select(.podUid==$uid)]|length)==0' \
      "${M10E_OUTPUT}/${current_stem}-topology-deleted.json" >/dev/null; then gone=true; break; fi
    sleep 1
  done
  [[ "${gone}" == true ]]
}

for pair in $(seq 1 "${M10E_PAIRS}"); do
  if (( pair % 2 == 1 )); then order=(runc kata); else order=(kata runc); fi
  position=0
  for runtime in "${order[@]}"; do
    position=$((position+1))
    measure_case "${pair}" "${position}" "${runtime}"
  done
done

python3 hack/summarize-m10e.py "${M10E_OUTPUT}" >"${M10E_OUTPUT}/summary.json"
get /metrics >"${M10E_OUTPUT}/metrics-after.txt"
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${M10E_OUTPUT}/agent-after.json"
jq -S '[.items[]|select(.kind=="Pod")|{uid:.metadata.uid,containers:([.status.containerStatuses[]|{name,restartCount,ready,containerID}]|sort_by(.name))}]|sort_by(.uid)' \
  "${M10E_OUTPUT}/agent-before.json" >"${M10E_OUTPUT}/agent-state-before.json"
jq -S '[.items[]|select(.kind=="Pod")|{uid:.metadata.uid,containers:([.status.containerStatuses[]|{name,restartCount,ready,containerID}]|sort_by(.name))}]|sort_by(.uid)' \
  "${M10E_OUTPUT}/agent-after.json" >"${M10E_OUTPUT}/agent-state-after.json"
cmp "${M10E_OUTPUT}/agent-state-before.json" "${M10E_OUTPUT}/agent-state-after.json"
events_lost_before="$(awk '$1=="kno_agent_events_lost_total" {value=$2;count++} END {if(count!=1) exit 1;print value}' "${M10E_OUTPUT}/metrics-before.txt")"
events_lost_after="$(awk '$1=="kno_agent_events_lost_total" {value=$2;count++} END {if(count!=1) exit 1;print value}' "${M10E_OUTPUT}/metrics-after.txt")"
[[ "${events_lost_after}" == "${events_lost_before}" ]]
echo "M10e runc/Kata paired captures are valid; effect sizes remain descriptive."
