#!/usr/bin/env bash
# Own one namespace and temporarily replace the Agent; always restore its spec.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
AGENT_IMAGE="${AGENT_IMAGE:-kubenode-observer/kno-agent:guest}"
GUEST_OUTPUT="${GUEST_OUTPUT:-log/guest-test-$(date -u +%Y%m%dT%H%M%SZ)}"
export AGENT_IMAGE
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
[[ -z "$(kube get ns kno-guest-test --ignore-not-found -o name)" ]]
kube get runtimeclass kno-kata-qemu-guest >/dev/null
mkdir -- "${GUEST_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${GUEST_OUTPUT}"
owned=false;restore=false;forward="";burn=""
cleanup() {
  code=$?;trap - EXIT
  if [[ "${code}" != 0 && "${owned}" == true ]];then
    kube -n kno-guest-test get pods -o json >"${GUEST_OUTPUT}/failure-pods.json" || true
    kube -n kno-guest-test describe pods >"${GUEST_OUTPUT}/failure-describe.txt" || true
    kube -n kno-guest-test logs kno-guest -c probe >"${GUEST_OUTPUT}/failure-probe.txt" || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${GUEST_OUTPUT}/failure-agent.txt" || true
  fi
  if [[ -n "${forward}" ]];then kill "${forward}" 2>/dev/null || true;wait "${forward}" 2>/dev/null || true;fi
  if [[ "${owned}" == true ]];then kube delete ns kno-guest-test --wait=true --timeout=90s || code=1;fi
  if [[ -n "${burn}" ]];then wait "${burn}" 2>/dev/null || true;fi
  if [[ "${restore}" == true ]];then
    kube apply -f "${GUEST_OUTPUT}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=90s || code=1
    kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${GUEST_OUTPUT}/restored-agent.json" || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${GUEST_OUTPUT}/restored-spec.json" || code=1
    jq -S .spec "${GUEST_OUTPUT}/original-agent.json" >"${GUEST_OUTPUT}/original-spec.json"
    cmp "${GUEST_OUTPUT}/original-spec.json" "${GUEST_OUTPUT}/restored-spec.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${GUEST_OUTPUT}/exit.json";exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD >"${GUEST_OUTPUT}/source-head.txt"
sha256sum internal/guest/*.go cmd/kno-guest/main.go cmd/kno-agent/main.go internal/probe/runqlat.go >"${GUEST_OUTPUT}/source-sha256.txt"
docker image inspect kubenode-observer/guest-probe:dev "${AGENT_IMAGE}" kubenode-observer/guest-workload:dev >"${GUEST_OUTPUT}/images.json"
kube -n kube-system get ds kno-agent -o json >"${GUEST_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${GUEST_OUTPUT}/original-agent.json" >"${GUEST_OUTPUT}/original-manifest.json"
jq '.spec.template.spec.containers[0].image=env.AGENT_IMAGE | .spec.template.spec.containers[0].args += ["--guest-probe-port=18081"]' "${GUEST_OUTPUT}/original-manifest.json" >"${GUEST_OUTPUT}/candidate.json"
restore=true
kube apply -f "${GUEST_OUTPUT}/candidate.json"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${GUEST_OUTPUT}/before-agent.json"
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${GUEST_OUTPUT}/forward.txt" 2>&1 &
forward=$!
endpoint=""
for _ in $(seq 1 100);do address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${GUEST_OUTPUT}/forward.txt")";if [[ -n "${address}" ]];then endpoint="http://${address}";break;fi;sleep .1;done
[[ -n "${endpoint}" ]]
get() { curl --fail-with-body -sS --connect-timeout 2 --max-time 20 "${endpoint}$1"; }
# Guest routing requires informer identity and Pod IP, unlike CRI-only fallback.
informer_ready=false
for _ in $(seq 1 30);do
  get '/api/v1/topology' >"${GUEST_OUTPUT}/informer-preflight.json"
  if jq -e '(.snapshot.nodeUid//"")!=""' "${GUEST_OUTPUT}/informer-preflight.json" >/dev/null;then informer_ready=true;break;fi
  sleep 1
done
if [[ "${informer_ready}" != true ]];then echo 'Kubernetes informer unavailable: check Node Service VIP/API connectivity before creating a Guest VM.' >&2;exit 1;fi
get /metrics >"${GUEST_OUTPUT}/metrics-before.txt"
kube create ns kno-guest-test;owned=true
kube apply -f examples/guest-probe/pods.yaml
kube -n kno-guest-test wait --for=condition=Ready pod --all --timeout=180s
kube -n kno-guest-test get pods -o json >"${GUEST_OUTPUT}/pods.json"
ready=false
for attempt in $(seq 1 30);do
  get '/api/v1/topology?namespace=kno-guest-test' >"${GUEST_OUTPUT}/topology-${attempt}.json"
  if jq -e '(.snapshot.sandboxes|length)==1 and (.snapshot.sandboxes[0] | .hypervisor.pid>0 and (.issues//[]|length)==0)' "${GUEST_OUTPUT}/topology-${attempt}.json" >/dev/null;then ready=true;break;fi;sleep 1
done
[[ "${ready}" == true ]]
cp "${GUEST_OUTPUT}/topology-${attempt}.json" "${GUEST_OUTPUT}/topology-ready.json"
sandbox="$(jq -r '.snapshot.sandboxes[0].id' "${GUEST_OUTPUT}/topology-ready.json")"
uid="$(kube -n kno-guest-test get pod kno-guest -o jsonpath='{.metadata.uid}')"
pod_ip="$(kube -n kno-guest-test get pod kno-guest -o jsonpath='{.status.podIP}')"
node curl -fsS --max-time 10 "http://${pod_ip}:18081/api/v1/guest/resources?duration=100ms" >"${GUEST_OUTPUT}/direct-probe.json"
jq -e --arg uid "${uid}" '.domain=="guest" and .podUid==$uid and (.sandboxId//"")==""' "${GUEST_OUTPUT}/direct-probe.json" >/dev/null
node sha256sum /opt/kata/share/kata-containers/vmlinux-debug.container /opt/kata/share/kata-containers/config-6.18.56-208-debug >"${GUEST_OUTPUT}/guest-kernel-sha256.txt"
for phase in baseline contention recovery;do
  workers=1;[[ "${phase}" != contention ]] || workers=8
  kube -n kno-guest-test exec kno-guest -c workload -- /guest-burn --workers="${workers}" --duration=60s >"${GUEST_OUTPUT}/${phase}-workload.txt" 2>&1 &
  burn=$!
  valid=false
  for attempt in $(seq 1 8);do
    get "/api/v1/guest/resources?sandbox=${sandbox}&comm=guest-burn&duration=1s" >"${GUEST_OUTPUT}/${phase}-warmup-${attempt}.json"
    if jq -e '(.processes|length)==1 and (.processes[0] | .cpu!=null and .runqlat!=null and .runqlat.count>0)' "${GUEST_OUTPUT}/${phase}-warmup-${attempt}.json" >/dev/null;then valid=true;break;fi;sleep 1
  done
  [[ "${valid}" == true ]]
  for round in 1 2;do
    stable=false
    for attempt in 1 2 3;do
      sample="${GUEST_OUTPUT}/${phase}-${round}-attempt-${attempt}.json"
      ./bin/kno --endpoint "${endpoint}" guest resources --sandbox "${sandbox}" --comm guest-burn --duration=5s >"${sample}"
      if jq -e --arg uid "${uid}" --arg sid "${sandbox}" '.domain=="guest" and .podUid==$uid and .sandboxId==$sid and (.processes|length)==1 and (.processes[0] | .command=="guest-burn" and .cpu.percent>0 and .runqlat.count>0 and .runqlatCoverage.observedThreads>0 and .runqlatCoverage.observedThreads<=.runqlatCoverage.totalThreads and (.partial==(.runqlatCoverage.observedThreads<.runqlatCoverage.totalThreads)))' "${sample}" >/dev/null;then
        cp "${sample}" "${GUEST_OUTPUT}/${phase}-${round}.json";stable=true;break
      fi
      # Preserve unstable windows; do not select on quantile magnitude.
      sleep 1
    done
    [[ "${stable}" == true ]]
  done
  get "/api/v1/host/resources?namespace=kno-guest-test&pod=kno-guest&kind=kata&duration=1s" >"${GUEST_OUTPUT}/${phase}-host.json"
  wait "${burn}";burn=""
done
jq -s '{baselineWindowP95Us:[.[0:2][]|.processes[0].runqlat.p95Us],contentionWindowP95Us:[.[2:4][]|.processes[0].runqlat.p95Us],recoveryWindowP95Us:[.[4:6][]|.processes[0].runqlat.p95Us]}' "${GUEST_OUTPUT}/baseline-1.json" "${GUEST_OUTPUT}/baseline-2.json" "${GUEST_OUTPUT}/contention-1.json" "${GUEST_OUTPUT}/contention-2.json" "${GUEST_OUTPUT}/recovery-1.json" "${GUEST_OUTPUT}/recovery-2.json" >"${GUEST_OUTPUT}/effects.json"
# Exploratory trend, NOT a pass/fail gate: noisy quantiles must not cause a
# whole experiment to be silently repeated until a desired effect appears.
jq '(.baselineWindowP95Us|add/length) as $b | (.contentionWindowP95Us|add/length) as $c | (.recoveryWindowP95Us|add/length) as $r | {baselineMeanWindowP95Us:$b,contentionMeanWindowP95Us:$c,recoveryMeanWindowP95Us:$r,contentionAbove2xBaseline:($c>2*$b),recoveryBelowContention:($r<$c)}' "${GUEST_OUTPUT}/effects.json" >"${GUEST_OUTPUT}/trend.json"
kube -n kno-guest-test exec kno-guest -c probe -- /kno-guest --inspect >"${GUEST_OUTPUT}/probe-resources.json"
kube -n kno-guest-test get pods -o json >"${GUEST_OUTPUT}/pods-after.json"
jq -e '[.items[].status.containerStatuses[] | .restartCount==0 and .ready] | all' "${GUEST_OUTPUT}/pods-after.json" >/dev/null
jq -e '."memory.events" | test("oom 0\\noom_kill 0")' "${GUEST_OUTPUT}/probe-resources.json" >/dev/null
kube -n kno-guest-test exec kno-guest-runc -- /guest-burn --workers=1 --duration=1s >"${GUEST_OUTPUT}/runc-positive.txt"
get '/api/v1/events?namespace=kno-guest-test&limit=1000' >"${GUEST_OUTPUT}/boundary-events.json"
jq -e '([.events[]|select(.comm=="guest-burn" and .type=="exec" and .workload.podName=="kno-guest-runc")]|length)>0 and ([.events[]|select(.comm=="guest-burn" and .workload.podName=="kno-guest")]|length)==0' "${GUEST_OUTPUT}/boundary-events.json" >/dev/null
get '/api/v1/cpu?namespace=kno-guest-test&pod=kno-guest&level=process' >"${GUEST_OUTPUT}/legacy-host-cpu.json"
jq -e '(.items|length)==0' "${GUEST_OUTPUT}/legacy-host-cpu.json" >/dev/null
kube -n kno-guest-test delete pod kno-guest --wait=true --timeout=90s
for _ in $(seq 1 30);do
  get '/api/v1/topology?namespace=kno-guest-test' >"${GUEST_OUTPUT}/topology-deleted.json"
  if jq -e '(.snapshot.sandboxes|length)==0' "${GUEST_OUTPUT}/topology-deleted.json" >/dev/null;then break;fi;sleep 1
done
code="$(curl -sS -o "${GUEST_OUTPUT}/after-delete.json" -w '%{http_code}' "${endpoint}/api/v1/guest/resources?sandbox=${sandbox}&duration=100ms")"
[[ "${code}" == 404 ]]
get /metrics >"${GUEST_OUTPUT}/metrics-after.txt"
awk '/^kno_agent_events_lost_total / {if ($2!=0) bad=1; found=1} /^kno_agent_bpf_map_stats_ready\{/ {if ($2!=1) bad=1; ready++} /^kno_agent_bpf_map_stats_refresh_errors_total\{/ {if ($2!=0) bad=1; sources++} /^kno_agent_metrics_collection_errors / {if ($2!=0) bad=1} END {if (bad || !found || ready!=3 || sources!=3) exit 1}' "${GUEST_OUTPUT}/metrics-after.txt"
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${GUEST_OUTPUT}/after-agent.json"
jq -e --slurpfile b "${GUEST_OUTPUT}/before-agent.json" '([.items[]|select(.kind=="Pod")][0]) as $p | $p.metadata.uid==([$b[0].items[]|select(.kind=="Pod")][0].metadata.uid) and $p.status.containerStatuses[0].restartCount==0' "${GUEST_OUTPUT}/after-agent.json"
echo 'M9 passed: real Guest CPU/runqlat over Pod network, authoritative Sandbox join, distinct domains and deletion cleanup.'
