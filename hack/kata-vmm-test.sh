#!/usr/bin/env bash
# Invoked inside kata-test.sh's owned namespace and experiment lock. No QMP
# commands, sysctl changes, global affinities or persistent workloads.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
: "${KATA_OUTPUT:?}" "${KATA_TEST_ENDPOINT:?}" "${KATA_TEST_SANDBOX:?}"
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
get() { curl --connect-timeout 2 --max-time 20 -fsS "${KATA_TEST_ENDPOINT}$1"; }
vmm="$(jq -r '.snapshot.sandboxes[0].hypervisor.pid' "${KATA_OUTPUT}/topology-ready.json")"
node /opt/kata/bin/qemu-system-x86_64 --version >"${KATA_OUTPUT}/m8-qemu-version.txt"
node taskset -apc "${vmm}" >"${KATA_OUTPUT}/m8-affinity-before.txt"
cpu="$(node awk '/^Cpus_allowed_list:/ {split($2,a,/[-,]/);print a[1]}' "/proc/${vmm}/status")"
node taskset -apc "${cpu}" "${vmm}" >"${KATA_OUTPUT}/m8-affinity-pinned.txt"
git rev-parse HEAD >"${KATA_OUTPUT}/m8-source-head.txt"
git diff --binary >"${KATA_OUTPUT}/m8-source.patch"
sha256sum bpf/cpu.bpf.c internal/probe/cpu.go internal/host/*.go hack/kata-vmm-test.sh >"${KATA_OUTPUT}/m8-source-sha256.txt"
# Prime the observation window; retain each failure/partial result separately.
ready=false
for attempt in $(seq 1 8); do
  get '/api/v1/host/resources?namespace=kno-kata-test&duration=1s' >"${KATA_OUTPUT}/m8-warmup-${attempt}.json"
  if jq -e '([.items[]|select(.kind=="kata-sandbox")]|length)==1 and ([.items[]|select(.kind=="kata-sandbox")][0] | .cpu!=null and .vcpuRunqlat!=null and ([.threads[]|select(.role=="vcpu")]|length)>0)' "${KATA_OUTPUT}/m8-warmup-${attempt}.json" >/dev/null; then ready=true;break;fi
  sleep 1
done
[[ "${ready}" == true ]]
measure() {
  local phase="$1" round="$2"
  docker exec -i "${MINIKUBE_PROFILE}" python3 - "${vmm}" < hack/vmm-reference.py >"${KATA_OUTPUT}/m8-${phase}-${round}-reference-start.json"
  ./bin/kno --endpoint "${KATA_TEST_ENDPOINT}" top workloads -n kno-kata-test --duration=5s >"${KATA_OUTPUT}/m8-${phase}-${round}.json"
  docker exec -i "${MINIKUBE_PROFILE}" python3 - "${vmm}" < hack/vmm-reference.py >"${KATA_OUTPUT}/m8-${phase}-${round}-reference-end.json"
  jq -e --arg sid "${KATA_TEST_SANDBOX}" '.domain=="host" and ([.items[]|select(.kind=="kata-sandbox" and .sandboxId==$sid)] | length)==1 and ([.items[]|select(.kind=="kata-sandbox")][0] | .cpu.runtimeNs>0 and .memory.source=="proc-status-rss" and .io!=null and .vcpuRunqlat.count>0 and ([.threads[]|select(.role=="vcpu" and .cpu!=null and .kernelTid>0)]|length)>0) and ([.items[]|select(.kind=="runc-container")]|length)>0' "${KATA_OUTPUT}/m8-${phase}-${round}.json" >/dev/null
}
measure baseline 1
measure baseline 2
kube apply -f examples/kata-boundary/contention.yaml
kube -n kno-kata-test wait --for=condition=Ready pod/kno-host-contention --timeout=60s
cid="$(kube -n kno-kata-test get pod kno-host-contention -o jsonpath='{.status.containerStatuses[0].containerID}')"
node crictl inspect "${cid#containerd://}" >"${KATA_OUTPUT}/m8-contention-cri.json"
contender="$(jq -r '.info.pid' "${KATA_OUTPUT}/m8-contention-cri.json")"
node taskset -apc "${cpu}" "${contender}" >"${KATA_OUTPUT}/m8-contention-affinity.txt"
measure contention 1
measure contention 2
kube -n kno-kata-test delete pod kno-host-contention --wait=true --timeout=60s
measure recovery 1
measure recovery 2
jq -s '{baselineP95Us:([.[0:2][]|.items[]|select(.kind=="kata-sandbox")|.vcpuRunqlat.p95Us]|add/length),contentionP95Us:([.[2:4][]|.items[]|select(.kind=="kata-sandbox")|.vcpuRunqlat.p95Us]|add/length),recoveryP95Us:([.[4:6][]|.items[]|select(.kind=="kata-sandbox")|.vcpuRunqlat.p95Us]|add/length)}' "${KATA_OUTPUT}/m8-baseline-1.json" "${KATA_OUTPUT}/m8-baseline-2.json" "${KATA_OUTPUT}/m8-contention-1.json" "${KATA_OUTPUT}/m8-contention-2.json" "${KATA_OUTPUT}/m8-recovery-1.json" "${KATA_OUTPUT}/m8-recovery-2.json" >"${KATA_OUTPUT}/m8-effects.json"
jq -e '.contentionP95Us > .baselineP95Us*2 and .recoveryP95Us < .contentionP95Us' "${KATA_OUTPUT}/m8-effects.json" >/dev/null
code="$(curl -sS -o "${KATA_OUTPUT}/m8-invalid.json" -w '%{http_code}' "${KATA_TEST_ENDPOINT}/api/v1/host/resources?duration=0s")"
[[ "${code}" == 400 ]]
get '/api/v1/host/resources?namespace=kno-kata-test&duration=2s' >"${KATA_OUTPUT}/m8-concurrent-first.json" &
request_pid=$!
sleep .3
code="$(curl -sS -o "${KATA_OUTPUT}/m8-busy.json" -w '%{http_code}' "${KATA_TEST_ENDPOINT}/api/v1/host/resources?duration=100ms")"
wait "${request_pid}"
[[ "${code}" == 429 ]]
echo 'M8 passed: real namespace joins, unified Host CPU/memory/IO, vCPU scheduling contention/recovery and HTTP error semantics.'
