#!/usr/bin/env bash
# M10a only: short-lived calibration/identity tests, not cross-layer attribution.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
CLOCK_OUTPUT="${CLOCK_OUTPUT:-log/clock-test-$(date -u +%Y%m%dT%H%M%SZ)}"
CLOCK_AGENT_IMAGE="${CLOCK_AGENT_IMAGE:-kubenode-observer/kno-agent:clock}"
CLOCK_GUEST_IMAGE="${CLOCK_GUEST_IMAGE:-kubenode-observer/guest-probe:clock}"
export CLOCK_AGENT_IMAGE CLOCK_GUEST_IMAGE
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
[[ -z "$(kube get ns kno-clock-test --ignore-not-found -o name)" ]]
kube get runtimeclass kno-kata-qemu-guest >/dev/null
mkdir -- "${CLOCK_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${CLOCK_OUTPUT}"
owned=false;restore=false;forward="";burn=""
cleanup() {
  code=$?;trap - EXIT
  if [[ "${code}" != 0 && "${owned}" == true ]];then
    kube -n kno-clock-test get pods -o json >"${CLOCK_OUTPUT}/failure-pods.json" || true
    kube -n kno-clock-test logs kno-guest -c probe >"${CLOCK_OUTPUT}/failure-probe.txt" || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${CLOCK_OUTPUT}/failure-agent.txt" || true
  fi
  if [[ -n "${forward}" ]];then kill "${forward}" 2>/dev/null || true;wait "${forward}" 2>/dev/null || true;fi
  if [[ "${owned}" == true ]];then kube delete ns kno-clock-test --wait=true --timeout=90s || code=1;fi
  if [[ -n "${burn}" ]];then wait "${burn}" 2>/dev/null || true;fi
  if [[ "${restore}" == true ]];then
    kube apply -f "${CLOCK_OUTPUT}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=90s || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${CLOCK_OUTPUT}/restored-spec.json" || code=1
    jq -S .spec "${CLOCK_OUTPUT}/original-agent.json" >"${CLOCK_OUTPUT}/original-spec.json"
    cmp "${CLOCK_OUTPUT}/original-spec.json" "${CLOCK_OUTPUT}/restored-spec.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${CLOCK_OUTPUT}/exit.json";exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD >"${CLOCK_OUTPUT}/source-head.txt"
sha256sum internal/guest/clock*.go cmd/kno/clock.go cmd/kno-agent/main.go cmd/kno-guest/main.go >"${CLOCK_OUTPUT}/source-sha256.txt"
docker image inspect "${CLOCK_AGENT_IMAGE}" "${CLOCK_GUEST_IMAGE}" kubenode-observer/guest-workload:dev >"${CLOCK_OUTPUT}/images.json"
kube -n kube-system get ds kno-agent -o json >"${CLOCK_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${CLOCK_OUTPUT}/original-agent.json" >"${CLOCK_OUTPUT}/original-manifest.json"
jq '.spec.template.spec.containers[0].image=env.CLOCK_AGENT_IMAGE' "${CLOCK_OUTPUT}/original-manifest.json" >"${CLOCK_OUTPUT}/disabled-manifest.json"
restore=true;kube apply -f "${CLOCK_OUTPUT}/disabled-manifest.json"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
start_forward() {
  kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${CLOCK_OUTPUT}/forward.txt" 2>&1 & forward=$!
  endpoint=""
  for _ in $(seq 1 100);do address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${CLOCK_OUTPUT}/forward.txt")";if [[ -n "${address}" ]];then endpoint="http://${address}";break;fi;sleep .1;done
  [[ -n "${endpoint}" ]]
}
start_forward
fake=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
code="$(curl -sS -o "${CLOCK_OUTPUT}/disabled.json" -w '%{http_code}' "${endpoint}/api/v1/guest/clock?sandbox=${fake}")";[[ "${code}" == 503 ]]
kill "${forward}";wait "${forward}" 2>/dev/null || true;forward=""
jq '.spec.template.spec.containers[0].args += ["--guest-probe-port=18081"]' "${CLOCK_OUTPUT}/disabled-manifest.json" >"${CLOCK_OUTPUT}/candidate.json"
kube apply -f "${CLOCK_OUTPUT}/candidate.json";kube -n kube-system rollout status ds/kno-agent --timeout=90s
start_forward
get() { curl --fail-with-body -sS --connect-timeout 2 --max-time 10 "${endpoint}$1"; }
ready=false
for _ in $(seq 1 30);do get /api/v1/topology >"${CLOCK_OUTPUT}/informer.json";if jq -e '(.snapshot.nodeUid//"")!=""' "${CLOCK_OUTPUT}/informer.json" >/dev/null;then ready=true;break;fi;sleep 1;done
[[ "${ready}" == true ]]
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${CLOCK_OUTPUT}/agent-before.json"
get /metrics >"${CLOCK_OUTPUT}/metrics-before.txt"
kube create ns kno-clock-test;owned=true
kube create --dry-run=client -f examples/guest-probe/pods.yaml -o json | jq -s '{apiVersion:"v1",kind:"List",items:[.[]|select(.metadata.name=="kno-guest")|.metadata.namespace="kno-clock-test"|(.spec.containers[].image)|=if .=="kubenode-observer/guest-probe:dev" then env.CLOCK_GUEST_IMAGE else . end]}' >"${CLOCK_OUTPUT}/pod-manifest.json"
kube apply -f "${CLOCK_OUTPUT}/pod-manifest.json"
discover() {
  kube -n kno-clock-test wait --for=condition=Ready pod/kno-guest --timeout=180s
  ready=false
  for _ in $(seq 1 30);do get '/api/v1/topology?namespace=kno-clock-test' >"${CLOCK_OUTPUT}/topology-${1}.json";if jq -e '(.snapshot.sandboxes|length)==1 and (.snapshot.sandboxes[0]|.hypervisor.pid>0 and (.issues//[]|length)==0)' "${CLOCK_OUTPUT}/topology-${1}.json" >/dev/null;then ready=true;break;fi;sleep 1;done
  [[ "${ready}" == true ]]
  sandbox="$(jq -r '.snapshot.sandboxes[0].id' "${CLOCK_OUTPUT}/topology-${1}.json")"
}
discover initial
ip="$(kube -n kno-clock-test get pod kno-guest -o jsonpath='{.status.podIP}')"
docker exec "${MINIKUBE_PROFILE}" curl -fsS "http://${ip}:18081/api/v1/guest/clock" >"${CLOCK_OUTPUT}/direct.json"
for phase in idle loaded;do
  if [[ "${phase}" == loaded ]];then kube -n kno-clock-test exec kno-guest -c workload -- /guest-burn --workers=8 --duration=15s >"${CLOCK_OUTPUT}/workload.txt" 2>&1 & burn=$!;sleep 1;fi
  for round in 1 2 3 4 5;do ./bin/kno --endpoint "${endpoint}" guest clock --sandbox "${sandbox}" --samples=7 >"${CLOCK_OUTPUT}/${phase}-${round}.json";done
  if [[ -n "${burn}" ]];then
    get "/api/v1/guest/resources?sandbox=${sandbox}&comm=guest-burn&duration=1s" >"${CLOCK_OUTPUT}/guest-resources.json"
    get "/api/v1/host/resources?sandbox=${sandbox}&duration=1s" >"${CLOCK_OUTPUT}/host-resources.json"
    wait "${burn}";burn=""
  fi
done
kube -n kno-clock-test exec kno-guest -c probe -- /kno-guest --inspect >"${CLOCK_OUTPUT}/probe-resources.json"
kube -n kno-clock-test get pods -o json >"${CLOCK_OUTPUT}/pods.json"
old="${sandbox}"
kube -n kno-clock-test delete pod kno-guest --wait=true --timeout=90s
for _ in $(seq 1 30);do get '/api/v1/topology?namespace=kno-clock-test' >"${CLOCK_OUTPUT}/deleted-topology.json";if jq -e '(.snapshot.sandboxes|length)==0' "${CLOCK_OUTPUT}/deleted-topology.json" >/dev/null;then break;fi;sleep 1;done
code="$(curl -sS -o "${CLOCK_OUTPUT}/deleted.json" -w '%{http_code}' "${endpoint}/api/v1/guest/clock?sandbox=${old}")";[[ "${code}" == 404 ]]
kube apply -f "${CLOCK_OUTPUT}/pod-manifest.json";discover recreated
[[ "${sandbox}" != "${old}" ]]
./bin/kno --endpoint "${endpoint}" guest clock --sandbox "${sandbox}" --samples=15 >"${CLOCK_OUTPUT}/recreated.json"
jq -e --slurpfile old "${CLOCK_OUTPUT}/idle-1.json" '.podUid!=$old[0].podUid and .sandboxId!=$old[0].sandboxId and .guest.bootId!=$old[0].guest.bootId and .probeInstanceId!=$old[0].probeInstanceId' "${CLOCK_OUTPUT}/recreated.json" >/dev/null
get /metrics >"${CLOCK_OUTPUT}/metrics-after.txt"
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${CLOCK_OUTPUT}/agent-after.json"
jq -e --slurpfile b "${CLOCK_OUTPUT}/agent-before.json" '.items[0].metadata.uid==$b[0].items[0].metadata.uid and .items[0].status.containerStatuses[0].restartCount==0' "${CLOCK_OUTPUT}/agent-after.json" >/dev/null
awk '/^kno_agent_events_lost_total / {if ($2!=0) bad=1; found=1} /^kno_agent_bpf_map_stats_ready\{/ {if ($2!=1) bad=1; ready++} /^kno_agent_bpf_map_stats_refresh_errors_total\{/ {if ($2!=0) bad=1; sources++} END {if (bad || !found || ready!=3 || sources!=3) exit 1}' "${CLOCK_OUTPUT}/metrics-after.txt"
python3 hack/validate-clock-contract.py "${CLOCK_OUTPUT}" >"${CLOCK_OUTPUT}/validation.json"
echo 'M10a clock calibration passed; no BPF alignment or cross-layer attribution claimed.'
