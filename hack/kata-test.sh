#!/usr/bin/env bash
# End-to-end host/guest visibility test on one Docker-driver Minikube node.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
AGENT_IMAGE="${AGENT_IMAGE:-kubenode-observer/kno-agent:kata-host}"
KATA_OUTPUT="${KATA_OUTPUT:-log/kata-test-$(date -u +%Y%m%dT%H%M%SZ)}"
export AGENT_IMAGE
export KUBE_CONTEXT MINIKUBE_PROFILE
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
bash hack/kata-preflight.sh
kube get runtimeclass kno-kata-qemu >/dev/null
[[ -z "$(kube get ns kno-kata-test --ignore-not-found -o name)" ]]
mkdir -- "${KATA_OUTPUT}"
git rev-parse HEAD >"${KATA_OUTPUT}/source-head.txt"
sha256sum internal/topology/{runtime,collector,kata,kata_monitor,model,store}.go cmd/kno-agent/main.go cmd/kno/main.go hack/kata-test.sh >"${KATA_OUTPUT}/source-sha256.txt"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${KATA_OUTPUT}"
forward_pid=""
namespace_created=false
restore=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ "${code}" != 0 && "${namespace_created}" == true ]]; then
    kube -n kno-kata-test get pods -o json >"${KATA_OUTPUT}/failure-pods.json" || true
    kube -n kno-kata-test describe pods >"${KATA_OUTPUT}/failure-describe.txt" || true
    node journalctl -u containerd -n 120 --no-pager >"${KATA_OUTPUT}/failure-containerd.txt" || true
    kube -n kube-system logs ds/kno-agent --tail=100 >"${KATA_OUTPUT}/failure-agent.txt" || true
  fi
  if [[ -n "${forward_pid}" ]]; then kill "${forward_pid}" 2>/dev/null || true; wait "${forward_pid}" 2>/dev/null || true; fi
  if [[ "${namespace_created}" == true ]]; then kube delete ns kno-kata-test --wait=true --timeout=90s || code=1; fi
  if [[ "${restore}" == true ]]; then
    kube apply -f "${KATA_OUTPUT}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=90s || code=1
    kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${KATA_OUTPUT}/restored-agent.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${KATA_OUTPUT}/exit.json"
  exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kube -n kube-system get ds kno-agent -o json >"${KATA_OUTPUT}/original-agent.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${KATA_OUTPUT}/original-agent.json" >"${KATA_OUTPUT}/original-manifest.json"
jq '.spec.template.spec.containers[0].image=env.AGENT_IMAGE | .spec.template.spec.containers[0].args += ["--kata-monitor=http://127.0.0.1:8090"]' "${KATA_OUTPUT}/original-manifest.json" >"${KATA_OUTPUT}/test-manifest.json"
restore=true
kube apply -f "${KATA_OUTPUT}/test-manifest.json"
kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${KATA_OUTPUT}/before-agent.json"
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${KATA_OUTPUT}/forward.txt" 2>&1 &
forward_pid=$!
endpoint=""
for _ in $(seq 1 100); do
  address="$(awk '/Forwarding from 127.0.0.1:/ {print $3; exit}' "${KATA_OUTPUT}/forward.txt")"
  if [[ -n "${address}" ]]; then endpoint="http://${address}"; break; fi
  sleep .1
done
[[ -n "${endpoint}" ]]
get() { curl --connect-timeout 2 --max-time 10 -fsS "${endpoint}$1"; }
get /healthz >"${KATA_OUTPUT}/health-before.json"
get /metrics >"${KATA_OUTPUT}/metrics-before.txt"
kube create ns kno-kata-test
namespace_created=true
kube apply -f examples/kata-boundary/pods.yaml
kube -n kno-kata-test wait --for=condition=Ready pod --all --timeout=180s
kube -n kno-kata-test get pods -o json >"${KATA_OUTPUT}/pods.json"
for runtime in runc kata; do
  kube -n kno-kata-test exec "kno-boundary-${runtime}" -c exec -- /workload --identity >"${KATA_OUTPUT}/${runtime}-identity.json"
done
[[ "$(jq -r .kernel "${KATA_OUTPUT}/runc-identity.json")" != "$(jq -r .kernel "${KATA_OUTPUT}/kata-identity.json")" ]]
ready=false
for attempt in $(seq 1 30); do
  get '/api/v1/topology?namespace=kno-kata-test' >"${KATA_OUTPUT}/topology-${attempt}.json"
  if jq -e '(.snapshot.containers|length)==3 and (.snapshot.sandboxes|length)==1 and (.snapshot.sandboxes[0] | .shim.pid>0 and .hypervisor.pid>0 and (.issues//[]|length)==0)' "${KATA_OUTPUT}/topology-${attempt}.json" >/dev/null; then ready=true; break; fi
  sleep 1
done
[[ "${ready}" == true ]]
cp "${KATA_OUTPUT}/topology-${attempt}.json" "${KATA_OUTPUT}/topology-ready.json"
jq -e '[.snapshot.containers[] | select(.podName=="kno-boundary-kata") | .processVisibility=="guest-unavailable" and .pid==0 and (.cgroupId//0)==0 and (.processes|length)==0] | length==2 and all' "${KATA_OUTPUT}/topology-ready.json" >/dev/null
jq -e --slurpfile pods "${KATA_OUTPUT}/pods.json" '.snapshot.sandboxes[0].podUid==([$pods[0].items[]|select(.metadata.name=="kno-boundary-kata")][0].metadata.uid)' "${KATA_OUTPUT}/topology-ready.json" >/dev/null
sandbox="$(jq -r '.snapshot.sandboxes[0].id' "${KATA_OUTPUT}/topology-ready.json")"
vmm="$(jq -r '.snapshot.sandboxes[0].hypervisor.pid' "${KATA_OUTPUT}/topology-ready.json")"
node crictl inspectp "${sandbox}" >"${KATA_OUTPUT}/cri-sandbox.json"
for container in exec cpu; do
  cid="$(kube -n kno-kata-test get pod kno-boundary-kata -o json | jq -r --arg name "${container}" '.status.containerStatuses[]|select(.name==$name)|.containerID|sub("^containerd://";"")')"
  node crictl inspect "${cid}" >"${KATA_OUTPUT}/cri-container-${container}.json"
done
node readlink "/proc/${vmm}/exe" >"${KATA_OUTPUT}/vmm-exe.txt"
node ls -l "/proc/${vmm}/fd" >"${KATA_OUTPUT}/vmm-fds.txt"
node curl -fsS "http://127.0.0.1:8090/sandboxes" >"${KATA_OUTPUT}/monitor-sandboxes.txt"
grep -Fx "${sandbox}" "${KATA_OUTPUT}/monitor-sandboxes.txt"
./bin/kno --endpoint "${endpoint}" kata metrics --sandbox "${sandbox}" >"${KATA_OUTPUT}/kata-metrics.txt"
grep -q '^kata_hypervisor_proc_stat' "${KATA_OUTPUT}/kata-metrics.txt"
grep -q '^kata_guest_cpu_time' "${KATA_OUTPUT}/kata-metrics.txt"
for runtime in runc kata; do
  for _ in $(seq 1 5); do kube -n kno-kata-test exec "kno-boundary-${runtime}" -c exec -- /kno-boundary --child; done
done
get '/api/v1/events?namespace=kno-kata-test&limit=1000' >"${KATA_OUTPUT}/boundary-events.json"
jq -e '([.events[] | select(.comm=="kno-boundary" and .type=="exec" and .workload.podName=="kno-boundary-runc")]|length)>0 and ([.events[] | select(.comm=="kno-boundary" and .workload.podName=="kno-boundary-kata")]|length)==0' "${KATA_OUTPUT}/boundary-events.json" >/dev/null
get '/api/v1/cpu?namespace=kno-kata-test&pod=kno-boundary-kata&level=process' >"${KATA_OUTPUT}/kata-host-cpu.json"
jq -e '(.items|length)==0' "${KATA_OUTPUT}/kata-host-cpu.json" >/dev/null
kube -n kno-kata-test delete pod kno-boundary-kata --wait=true --timeout=90s
removed=false
for attempt in $(seq 1 30); do
  get '/api/v1/topology?namespace=kno-kata-test' >"${KATA_OUTPUT}/topology-deleted-${attempt}.json"
  if jq -e '(.snapshot.sandboxes|length)==0 and (.snapshot.containers|length)==1' "${KATA_OUTPUT}/topology-deleted-${attempt}.json" >/dev/null; then removed=true; break; fi
  sleep 1
done
[[ "${removed}" == true ]]
gone=false
for _ in $(seq 1 30); do
  if node test ! -d "/proc/${vmm}"; then gone=true; break; fi
  sleep 1
done
[[ "${gone}" == true ]]
node curl -fsS 'http://127.0.0.1:8090/sandboxes' >"${KATA_OUTPUT}/monitor-after-delete.txt"
if grep -Fxq "${sandbox}" "${KATA_OUTPUT}/monitor-after-delete.txt"; then echo 'monitor retained deleted sandbox' >&2; exit 1; fi
kube -n kno-kata-test exec kno-boundary-runc -c exec -- /kno-boundary --child
get /metrics >"${KATA_OUTPUT}/metrics-after.txt"
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${KATA_OUTPUT}/after-agent.json"
jq -e --slurpfile before "${KATA_OUTPUT}/before-agent.json" '([.items[]|select(.kind=="Pod")][0]) as $p | $p.metadata.uid==([$before[0].items[]|select(.kind=="Pod")][0].metadata.uid) and $p.status.containerStatuses[0].restartCount==0 and $p.status.containerStatuses[0].ready' "${KATA_OUTPUT}/after-agent.json" >/dev/null
echo 'Kata boundary test passed: one VM for two containers, distinct guest kernel, host exec positive/guest negative control, official metrics, deletion cleanup, runc unchanged.'
