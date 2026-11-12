#!/usr/bin/env bash
# Practical M10c four-quadrant capture. Trends are reported, never used as gates.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
M10C_OUTPUT="${M10C_OUTPUT:-log/m10c-test-$(date -u +%Y%m%dT%H%M%SZ)}"
M10C_AGENT_IMAGE="${M10C_AGENT_IMAGE:-kubenode-observer/kno-agent:m10c}"
M10C_GUEST_IMAGE="${M10C_GUEST_IMAGE:-kubenode-observer/guest-probe:m10c}"
M10C_WORKLOAD_IMAGE="${M10C_WORKLOAD_IMAGE:-kubenode-observer/guest-workload:m10c}"
M10C_CONTENTION_IMAGE="${M10C_CONTENTION_IMAGE:-kubenode-observer/cpu-contention:dev}"
M10C_ROUNDS="${M10C_ROUNDS:-2}"
M10C_WINDOW_SECONDS="${M10C_WINDOW_SECONDS:-3}"
export M10C_AGENT_IMAGE M10C_GUEST_IMAGE M10C_WORKLOAD_IMAGE M10C_CONTENTION_IMAGE

[[ "${M10C_ROUNDS}" =~ ^[1-5]$ ]]
[[ "${M10C_WINDOW_SECONDS}" =~ ^([1-9]|10)$ ]]
kube(){ kubectl --context "${KUBE_CONTEXT}" "$@"; }
node(){ docker exec "${MINIKUBE_PROFILE}" "$@"; }
[[ -z "$(kube get ns kno-m10c-test --ignore-not-found -o name)" ]]
kube get runtimeclass kno-kata-qemu-guest >/dev/null
mkdir -- "${M10C_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${M10C_OUTPUT}"

owned=false;restore=false;forward="";burn="";host_request="";host_on=false;contender=""
cleanup(){
  code=$?;trap - EXIT
  if [[ -n "${host_request}" ]];then kill "${host_request}" 2>/dev/null || true;wait "${host_request}" 2>/dev/null || true;fi
  if [[ -n "${burn}" ]];then kill "${burn}" 2>/dev/null || true;wait "${burn}" 2>/dev/null || true;fi
  if [[ -n "${forward}" ]];then kill "${forward}" 2>/dev/null || true;wait "${forward}" 2>/dev/null || true;fi
  if [[ "${code}" != 0 && "${owned}" == true ]];then
    kube -n kno-m10c-test get pods -o json >"${M10C_OUTPUT}/failure-pods.json" || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${M10C_OUTPUT}/failure-agent.txt" || true
  fi
  if [[ "${owned}" == true ]];then kube delete ns kno-m10c-test --wait=true --timeout=120s || code=1;fi
  if [[ "${restore}" == true ]];then
    kube apply -f "${M10C_OUTPUT}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=120s || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${M10C_OUTPUT}/restored-spec.json" || code=1
    jq -S .spec "${M10C_OUTPUT}/original-agent.json" >"${M10C_OUTPUT}/original-spec.json"
    cmp "${M10C_OUTPUT}/original-spec.json" "${M10C_OUTPUT}/restored-spec.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${M10C_OUTPUT}/exit.json"
  exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

jq -n --arg createdAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg kubeContext "${KUBE_CONTEXT}" --arg minikubeProfile "${MINIKUBE_PROFILE}" \
  --arg agentImage "${M10C_AGENT_IMAGE}" --arg guestImage "${M10C_GUEST_IMAGE}" \
  --arg workloadImage "${M10C_WORKLOAD_IMAGE}" --arg contentionImage "${M10C_CONTENTION_IMAGE}" --argjson rounds "${M10C_ROUNDS}" \
  --argjson windowSeconds "${M10C_WINDOW_SECONDS}" \
  '{createdAt:$createdAt,kubeContext:$kubeContext,minikubeProfile:$minikubeProfile,images:{agent:$agentImage,guest:$guestImage,workload:$workloadImage,contention:$contentionImage},rounds:$rounds,windowSeconds:$windowSeconds}' \
  >"${M10C_OUTPUT}/config.json"

git rev-parse HEAD >"${M10C_OUTPUT}/source-head.txt"
# The workspace is shared by Windows Git and WSL. Match the Windows checkout's
# CRLF conversion so provenance captures real edits instead of an all-files EOL diff.
git -c core.autocrlf=true status --short --untracked-files=all \
  | grep -v '^?? ref-project/' \
  | grep -v '^?? log/m10c-test-' \
  >"${M10C_OUTPUT}/source-status.txt" || true
git -c core.autocrlf=true diff --binary -- . ':(exclude)ref-project/**' >"${M10C_OUTPUT}/source.patch"
sha256sum Makefile hack/m10c-test.sh hack/summarize-m10c.py examples/cpu-contention/main.go \
  examples/cpu-contention/Dockerfile.local examples/guest-probe/pods.yaml \
  examples/guest-probe/m10c-burn.c examples/guest-probe/Dockerfile.m10c-workload \
  examples/guest-probe/m10c-host-contention.yaml \
  internal/clockdomain/*.go internal/guest/*.go internal/host/*.go >"${M10C_OUTPUT}/source-sha256.txt"
docker image inspect "${M10C_AGENT_IMAGE}" "${M10C_GUEST_IMAGE}" "${M10C_WORKLOAD_IMAGE}" "${M10C_CONTENTION_IMAGE}" >"${M10C_OUTPUT}/images.json"

kube -n kube-system get ds kno-agent -o json >"${M10C_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${M10C_OUTPUT}/original-agent.json" >"${M10C_OUTPUT}/original-manifest.json"
jq '.spec.template.spec.containers[0].image=env.M10C_AGENT_IMAGE | .spec.template.spec.containers[0].args = ([.spec.template.spec.containers[0].args[] | select(startswith("--guest-probe-port=")|not)] + ["--guest-probe-port=18081"])' "${M10C_OUTPUT}/original-manifest.json" >"${M10C_OUTPUT}/candidate.json"
restore=true
kube apply -f "${M10C_OUTPUT}/candidate.json"
kube -n kube-system rollout status ds/kno-agent --timeout=120s
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${M10C_OUTPUT}/agent-before.json"

kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${M10C_OUTPUT}/forward.txt" 2>&1 & forward=$!
endpoint=""
for _ in $(seq 1 100);do
  address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${M10C_OUTPUT}/forward.txt")"
  if [[ -n "${address}" ]];then endpoint="http://${address}";break;fi
  sleep .1
done
[[ -n "${endpoint}" ]]
get(){ curl --fail-with-body -sS --connect-timeout 2 --max-time 30 "${endpoint}$1"; }
for _ in $(seq 1 30);do
  get /api/v1/topology >"${M10C_OUTPUT}/informer.json"
  jq -e '(.snapshot.nodeUid//"")!=""' "${M10C_OUTPUT}/informer.json" >/dev/null && break
  sleep 1
done
jq -e '(.snapshot.nodeUid//"")!=""' "${M10C_OUTPUT}/informer.json" >/dev/null
get /metrics >"${M10C_OUTPUT}/metrics-before.txt"

kube create ns kno-m10c-test;owned=true
kube create --dry-run=client -f examples/guest-probe/pods.yaml -o json | jq -s \
  '{apiVersion:"v1",kind:"List",items:[.[]|select(.metadata.name=="kno-guest")|.metadata.namespace="kno-m10c-test"|(.spec.containers[].image)|=if .=="kubenode-observer/guest-probe:dev" then env.M10C_GUEST_IMAGE elif .=="kubenode-observer/guest-workload:dev" then env.M10C_WORKLOAD_IMAGE else . end]}' \
  >"${M10C_OUTPUT}/guest-manifest.json"
kube apply -f "${M10C_OUTPUT}/guest-manifest.json"
kube -n kno-m10c-test wait --for=condition=Ready pod/kno-guest --timeout=180s
kube create --dry-run=client -f examples/guest-probe/m10c-host-contention.yaml -o json | \
  jq '.spec.containers[0].image=env.M10C_CONTENTION_IMAGE' >"${M10C_OUTPUT}/host-contention-manifest.json"

ready=false
for attempt in $(seq 1 30);do
  get '/api/v1/topology?namespace=kno-m10c-test' >"${M10C_OUTPUT}/topology-${attempt}.json"
  if jq -e '(.snapshot.sandboxes|length)==1 and (.snapshot.sandboxes[0]|.hypervisor.pid>0 and (.issues//[]|length)==0)' "${M10C_OUTPUT}/topology-${attempt}.json" >/dev/null;then ready=true;break;fi
  sleep 1
done
[[ "${ready}" == true ]]
cp "${M10C_OUTPUT}/topology-${attempt}.json" "${M10C_OUTPUT}/topology-ready.json"
sandbox="$(jq -r '.snapshot.sandboxes[0].id' "${M10C_OUTPUT}/topology-ready.json")"
vmm="$(jq -r '.snapshot.sandboxes[0].hypervisor.pid' "${M10C_OUTPUT}/topology-ready.json")"
get "/api/v1/host/resources?sandbox=${sandbox}&kind=kata&duration=1s" >"${M10C_OUTPUT}/host-setup.json"
jq -e '([.items[]|select(.kind=="kata-sandbox")]|length)==1 and ([.items[]|select(.kind=="kata-sandbox")][0].threads|map(select(.role=="vcpu" and .vcpuIndex==0))|length)==1' "${M10C_OUTPUT}/host-setup.json" >/dev/null
vcpu0="$(jq -r '[.items[]|select(.kind=="kata-sandbox")][0].threads[]|select(.role=="vcpu" and .vcpuIndex==0)|.tid' "${M10C_OUTPUT}/host-setup.json")"
[[ "${vcpu0}" =~ ^[1-9][0-9]*$ ]]
node taskset -pc "${vcpu0}" >"${M10C_OUTPUT}/vcpu0-affinity-before.txt"
cpu="$(node awk '/^Cpus_allowed_list:/ {split($2,a,/[-,]/);print a[1]}' "/proc/${vcpu0}/status")"
[[ "${cpu}" =~ ^[0-9]+$ ]]
node taskset -pc "${cpu}" "${vcpu0}" >"${M10C_OUTPUT}/vcpu0-affinity-pinned.txt"
jq -n --arg sandboxId "${sandbox}" --argjson vmmPid "${vmm}" --argjson vcpuIndex 0 \
  --argjson vcpuHostTid "${vcpu0}" --argjson pinnedHostCpu "${cpu}" \
  '{sandboxId:$sandboxId,vmmPid:$vmmPid,vcpuIndex:$vcpuIndex,vcpuHostTid:$vcpuHostTid,pinnedHostCpu:$pinnedHostCpu}' >"${M10C_OUTPUT}/setup.json"

set_host_contention(){
  local desired="$1"
  if [[ "${desired}" == 1 && "${host_on}" == false ]];then
    kube apply -f "${M10C_OUTPUT}/host-contention-manifest.json"
    kube -n kno-m10c-test wait --for=condition=Ready pod/kno-m10c-host-contention --timeout=60s
    cid="$(kube -n kno-m10c-test get pod kno-m10c-host-contention -o jsonpath='{.status.containerStatuses[0].containerID}')"
    node crictl inspect "${cid#containerd://}" >"${M10C_OUTPUT}/host-contention-cri.json"
    contender="$(jq -r '.info.pid' "${M10C_OUTPUT}/host-contention-cri.json")"
    [[ "${contender}" =~ ^[1-9][0-9]*$ ]]
    node taskset -apc "${cpu}" "${contender}" >"${M10C_OUTPUT}/host-contention-affinity.txt"
    contender_ready=false
    for _ in $(seq 1 50);do
      if kube -n kno-m10c-test logs kno-m10c-host-contention 2>/dev/null | grep -q '^running 4 worker(s)';then contender_ready=true;break;fi
      sleep .1
    done
    [[ "${contender_ready}" == true ]]
    kube -n kno-m10c-test logs kno-m10c-host-contention >"${M10C_OUTPUT}/host-contention-ready.txt"
    host_on=true
  elif [[ "${desired}" == 0 && "${host_on}" == true ]];then
    kube -n kno-m10c-test delete pod kno-m10c-host-contention --wait=true --timeout=60s
    host_on=false
  fi
  if [[ "${desired}" == 1 ]];then
    [[ "$(kube -n kno-m10c-test get pod kno-m10c-host-contention -o jsonpath='{.status.phase}')" == Running ]]
    node test -d "/proc/${contender}"
  fi
}

measure(){
  local round="$1" host_load="$2" guest_workers="$3"
  local stem
  stem="$(printf 'round-%02d-h%s-g%s' "${round}" "${host_load}" "${guest_workers}")"
  set_host_contention "${host_load}"
  if [[ "${host_load}" == 1 ]];then
    contention_uid="$(kube -n kno-m10c-test get pod kno-m10c-host-contention -o jsonpath='{.metadata.uid}')"
    jq -n --argjson hostContention true --argjson guestWorkers "${guest_workers}" \
      --arg podUid "${contention_uid}" --argjson contenderPid "${contender}" --argjson pinnedHostCpu "${cpu}" \
      '{hostContention:$hostContention,guestWorkers:$guestWorkers,hostContender:{podUid:$podUid,pid:$contenderPid,phase:"Running",pinnedHostCpu:$pinnedHostCpu}}' \
      >"${M10C_OUTPUT}/${stem}-condition.json"
  else
    [[ -z "$(kube -n kno-m10c-test get pod kno-m10c-host-contention --ignore-not-found -o name)" ]]
    jq -n --argjson hostContention false --argjson guestWorkers "${guest_workers}" \
      '{hostContention:$hostContention,guestWorkers:$guestWorkers,hostContender:null}' \
      >"${M10C_OUTPUT}/${stem}-condition.json"
  fi
  kube -n kno-m10c-test exec kno-guest -c workload -- /guest-burn --workers="${guest_workers}" --warmup=5s --duration="$((M10C_WINDOW_SECONDS+10))s" >"${M10C_OUTPUT}/${stem}-workload.txt" 2>&1 & burn=$!
  workload_ready=false
  for _ in $(seq 1 100);do
    if grep -q "^running ${guest_workers} worker(s) on CPU 0 " "${M10C_OUTPUT}/${stem}-workload.txt";then workload_ready=true;break;fi
    sleep .1
  done
  [[ "${workload_ready}" == true ]]
  sleep 2
  get "/api/v1/host/resources?sandbox=${sandbox}&kind=kata&duration=${M10C_WINDOW_SECONDS}s&timing=true" >"${M10C_OUTPUT}/${stem}-host.json" & host_request=$!
  get "/api/v1/guest/resources?sandbox=${sandbox}&comm=guest-burn&duration=${M10C_WINDOW_SECONDS}s&timing=true" >"${M10C_OUTPUT}/${stem}-guest.json"
  wait "${host_request}"
  host_request=""
  wait "${burn}";burn=""
}

for round in $(seq 1 "${M10C_ROUNDS}");do
  if (( round % 2 == 1 ));then order=("0 1" "0 8" "1 1" "1 8");else order=("1 8" "1 1" "0 8" "0 1");fi
  for pair in "${order[@]}";do read -r host_load guest_workers <<<"${pair}";measure "${round}" "${host_load}" "${guest_workers}";done
done
set_host_contention 0

python3 hack/summarize-m10c.py "${M10C_OUTPUT}" >"${M10C_OUTPUT}/summary.json"
get /metrics >"${M10C_OUTPUT}/metrics-after.txt"
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${M10C_OUTPUT}/agent-after.json"
jq -S '[.items[]|{uid:.metadata.uid,containers:([.status.containerStatuses[]|{name,restartCount,containerID}]|sort_by(.name))}]|sort_by(.uid)' "${M10C_OUTPUT}/agent-before.json" >"${M10C_OUTPUT}/agent-state-before.json"
jq -S '[.items[]|{uid:.metadata.uid,containers:([.status.containerStatuses[]|{name,restartCount,containerID}]|sort_by(.name))}]|sort_by(.uid)' "${M10C_OUTPUT}/agent-after.json" >"${M10C_OUTPUT}/agent-state-after.json"
cmp "${M10C_OUTPUT}/agent-state-before.json" "${M10C_OUTPUT}/agent-state-after.json"
events_lost_before="$(awk '$1=="kno_agent_events_lost_total" {value=$2;count++} END {if(count!=1) exit 1;print value}' "${M10C_OUTPUT}/metrics-before.txt")"
events_lost_after="$(awk '$1=="kno_agent_events_lost_total" {value=$2;count++} END {if(count!=1) exit 1;print value}' "${M10C_OUTPUT}/metrics-after.txt")"
[[ "${events_lost_after}" == "${events_lost_before}" ]]
echo "M10c four-quadrant captures are valid; inspect summary.json for descriptive trends and coverage warnings."
