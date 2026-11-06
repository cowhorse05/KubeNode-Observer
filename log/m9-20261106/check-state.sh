#!/usr/bin/env bash
# Read-only end-state checks after the serial experiments, no cleanup mutations.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
out=log/m9-20261106
kube() { kubectl --context kno-dev "$@"; }
[[ -z "$(kube get ns kno-guest-test kno-kata-test --ignore-not-found -o name)" ]]
[[ -z "$(kube -n kube-system get cm kno-experiment-lock --ignore-not-found -o name)" ]]
for pod in kno-exec-test-local kno-cpu-hog-local kno-runqlat-baseline kno-runqlat-contention kno-process-churn-local;do
  [[ -z "$(kube get pod "${pod}" --ignore-not-found -o name)" ]]
done
kube get nodes -o json >"${out}/final-nodes.json"
jq -e '[.items[].status.conditions[]|select(.type=="Ready")|.status=="True"]|length>0 and all' "${out}/final-nodes.json" >/dev/null
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${out}/final-agent.json"
jq -e '[.items[]|select(.kind=="Pod")|.status.containerStatuses[]|.ready and .restartCount==0]|length>0 and all' "${out}/final-agent.json" >/dev/null
kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${out}/final-agent-spec.json"
cmp log/guest-final-20261106/original-spec.json "${out}/final-agent-spec.json"
cmp log/m9-runc-20261106/original-spec.json "${out}/final-agent-spec.json"
docker exec kno-dev crictl info | jq '{defaultRuntime:.config.containerd.defaultRuntimeName,runtimes:(.config.containerd.runtimes|with_entries(.value={runtimeType:.value.runtimeType,options:.value.options}))}' >"${out}/final-runtimes.json"
jq -e '.defaultRuntime=="runc" and .runtimes["kata-qemu"].runtimeType=="io.containerd.kata.v2" and .runtimes["kata-qemu-guest"].runtimeType=="io.containerd.kata.v2"' "${out}/final-runtimes.json" >/dev/null
docker exec kno-dev crictl pods -o json | jq '[.items[]|select(.state=="SANDBOX_READY" and (.runtimeHandler|startswith("kata")))]' >"${out}/final-kata-sandboxes.json"
jq -e 'length==0' "${out}/final-kata-sandboxes.json" >/dev/null
printf '{"exitCode":0,"originalAgentSpecRestored":true,"testResourcesAbsent":true,"nodeReady":true,"defaultRuntime":"runc","dedicatedGuestRuntimeRetained":true}\n' >"${out}/final-state.json"
