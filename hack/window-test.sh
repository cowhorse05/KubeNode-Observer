#!/usr/bin/env bash
# M10b read-bracket/identity smoke, not a paired performance or causality study.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
WINDOW_OUTPUT="${WINDOW_OUTPUT:-log/window-test-$(date -u +%Y%m%dT%H%M%SZ)}"
WINDOW_AGENT_IMAGE="${WINDOW_AGENT_IMAGE:-kubenode-observer/kno-agent:window}"
WINDOW_GUEST_IMAGE="${WINDOW_GUEST_IMAGE:-kubenode-observer/guest-probe:window}"
export WINDOW_AGENT_IMAGE WINDOW_GUEST_IMAGE
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
[[ -z "$(kube get ns kno-window-test --ignore-not-found -o name)" ]]
kube get runtimeclass kno-kata-qemu-guest >/dev/null
mkdir -- "${WINDOW_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${WINDOW_OUTPUT}"
owned=false;restore=false;forward="";burn="";host_request=""
cleanup() {
  code=$?;trap - EXIT
  if [[ "${code}" != 0 && "${owned}" == true ]];then
    kube -n kno-window-test get pods -o json >"${WINDOW_OUTPUT}/failure-pods.json" || true
    kube -n kno-window-test logs kno-guest -c probe >"${WINDOW_OUTPUT}/failure-probe.txt" || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${WINDOW_OUTPUT}/failure-agent.txt" || true
  fi
  if [[ -n "${host_request}" ]];then kill "${host_request}" 2>/dev/null || true;wait "${host_request}" 2>/dev/null || true;fi
  if [[ -n "${forward}" ]];then kill "${forward}" 2>/dev/null || true;wait "${forward}" 2>/dev/null || true;fi
  if [[ "${owned}" == true ]];then kube delete ns kno-window-test --wait=true --timeout=90s || code=1;fi
  if [[ -n "${burn}" ]];then wait "${burn}" 2>/dev/null || true;fi
  if [[ "${restore}" == true ]];then
    kube apply -f "${WINDOW_OUTPUT}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=90s || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${WINDOW_OUTPUT}/restored-spec.json" || code=1
    jq -S .spec "${WINDOW_OUTPUT}/original-agent.json" >"${WINDOW_OUTPUT}/original-spec.json"
    cmp "${WINDOW_OUTPUT}/original-spec.json" "${WINDOW_OUTPUT}/restored-spec.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${WINDOW_OUTPUT}/exit.json";exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD >"${WINDOW_OUTPUT}/source-head.txt"
sha256sum internal/clockdomain/*.go internal/guest/*.go internal/host/*.go cmd/kno/main.go cmd/kno-agent/main.go cmd/kno-guest/main.go >"${WINDOW_OUTPUT}/source-sha256.txt"
docker image inspect "${WINDOW_AGENT_IMAGE}" "${WINDOW_GUEST_IMAGE}" kubenode-observer/guest-workload:dev >"${WINDOW_OUTPUT}/images.json"
kube -n kube-system get ds kno-agent -o json >"${WINDOW_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${WINDOW_OUTPUT}/original-agent.json" >"${WINDOW_OUTPUT}/original-manifest.json"
jq '.spec.template.spec.containers[0].image=env.WINDOW_AGENT_IMAGE | .spec.template.spec.containers[0].args = ([.spec.template.spec.containers[0].args[] | select(startswith("--guest-probe-port=")|not)] + ["--guest-probe-port=18081"])' "${WINDOW_OUTPUT}/original-manifest.json" >"${WINDOW_OUTPUT}/candidate.json"
restore=true;kube apply -f "${WINDOW_OUTPUT}/candidate.json"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${WINDOW_OUTPUT}/forward.txt" 2>&1 & forward=$!
endpoint=""
for _ in $(seq 1 100);do address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${WINDOW_OUTPUT}/forward.txt")";if [[ -n "${address}" ]];then endpoint="http://${address}";break;fi;sleep .1;done
[[ -n "${endpoint}" ]]
get() { curl --fail-with-body -sS --connect-timeout 2 --max-time 20 "${endpoint}$1"; }
ready=false
for _ in $(seq 1 30);do get /api/v1/topology >"${WINDOW_OUTPUT}/informer.json";if jq -e '(.snapshot.nodeUid//"")!=""' "${WINDOW_OUTPUT}/informer.json" >/dev/null;then ready=true;break;fi;sleep 1;done
[[ "${ready}" == true ]]
get /metrics >"${WINDOW_OUTPUT}/metrics-before.txt"
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${WINDOW_OUTPUT}/agent-before.json"
kube create ns kno-window-test;owned=true
kube create --dry-run=client -f examples/guest-probe/pods.yaml -o json | jq -s '{apiVersion:"v1",kind:"List",items:[.[]|select(.metadata.name=="kno-guest")|.metadata.namespace="kno-window-test"|(.spec.containers[].image)|=if .=="kubenode-observer/guest-probe:dev" then env.WINDOW_GUEST_IMAGE else . end]}' >"${WINDOW_OUTPUT}/pod-manifest.json"
kube apply -f "${WINDOW_OUTPUT}/pod-manifest.json"
kube -n kno-window-test wait --for=condition=Ready pod/kno-guest --timeout=180s
ready=false
for _ in $(seq 1 30);do get '/api/v1/topology?namespace=kno-window-test' >"${WINDOW_OUTPUT}/topology.json";if jq -e '(.snapshot.sandboxes|length)==1 and (.snapshot.sandboxes[0]|.hypervisor.pid>0 and (.issues//[]|length)==0)' "${WINDOW_OUTPUT}/topology.json" >/dev/null;then ready=true;break;fi;sleep 1;done
[[ "${ready}" == true ]]
sandbox="$(jq -r '.snapshot.sandboxes[0].id' "${WINDOW_OUTPUT}/topology.json")"
get "/api/v1/guest/resources?sandbox=${sandbox}&duration=100ms" >"${WINDOW_OUTPUT}/guest-default.json"
get "/api/v1/host/resources?sandbox=${sandbox}&duration=100ms" >"${WINDOW_OUTPUT}/host-default.json"
get '/api/v1/host/resources?namespace=kno-window-test&pod=absent&duration=100ms&timing=true' >"${WINDOW_OUTPUT}/host-empty.json"
kube -n kno-window-test exec kno-guest -c workload -- /guest-burn --workers=4 --duration=30s >"${WINDOW_OUTPUT}/workload.txt" 2>&1 & burn=$!
ready=false
for _ in $(seq 1 50);do if grep -q '^running 4 worker(s)' "${WINDOW_OUTPUT}/workload.txt";then ready=true;break;fi;sleep .1;done
[[ "${ready}" == true ]]
# Fixed warmup, not retry-until-valid: Go may still grow its OS thread set just
# after worker readiness. Every measured round must pass the unchanged gate.
sleep 5
for round in 1 2 3;do
  ./bin/kno --endpoint "${endpoint}" guest clock --sandbox "${sandbox}" --samples=7 >"${WINDOW_OUTPUT}/pre-${round}.json"
  ./bin/kno --endpoint "${endpoint}" top workloads --namespace kno-window-test --duration=2s --timing >"${WINDOW_OUTPUT}/host-${round}.json" & host_request=$!
  ./bin/kno --endpoint "${endpoint}" guest resources --sandbox "${sandbox}" --comm=guest-burn --duration=2s --timing >"${WINDOW_OUTPUT}/guest-${round}.json"
  wait "${host_request}";host_request=""
  ./bin/kno --endpoint "${endpoint}" guest clock --sandbox "${sandbox}" --samples=7 >"${WINDOW_OUTPUT}/post-${round}.json"
done
wait "${burn}";burn=""
get /metrics >"${WINDOW_OUTPUT}/metrics-after.txt"
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${WINDOW_OUTPUT}/agent-after.json"
kube -n kno-window-test get pods -o json >"${WINDOW_OUTPUT}/pods.json"
jq -e --slurpfile b "${WINDOW_OUTPUT}/agent-before.json" '.items[0].metadata.uid==$b[0].items[0].metadata.uid and .items[0].status.containerStatuses[0].restartCount==0' "${WINDOW_OUTPUT}/agent-after.json" >/dev/null
awk '/^kno_agent_events_lost_total / {if ($2!=0) bad=1; found=1} /^kno_agent_bpf_map_stats_ready\{/ {if ($2!=1) bad=1; ready++} /^kno_agent_bpf_map_stats_refresh_errors_total\{/ {if ($2!=0) bad=1; sources++} END {if (bad || !found || ready!=3 || sources!=3) exit 1}' "${WINDOW_OUTPUT}/metrics-after.txt"
python3 hack/validate-window-contract.py "${WINDOW_OUTPUT}" >"${WINDOW_OUTPUT}/validation.json"
echo 'M10b snapshot brackets passed; no cross-domain window overlap claimed.'
