#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
out=log/m10-20261109
kube(){ kubectl --context kno-dev "$@"; }
[[ -z "$(kube get ns kno-clock-test kno-guest-test kno-kata-test --ignore-not-found -o name)" ]]
[[ -z "$(kube -n kube-system get cm kno-experiment-lock --ignore-not-found -o name)" ]]
for pod in kno-exec-test-local kno-cpu-hog-local kno-runqlat-baseline kno-runqlat-contention;do [[ -z "$(kube get pod "${pod}" --ignore-not-found -o name)" ]];done
kube get nodes -o json >"${out}/final-nodes.json"
jq -e '[.items[].status.conditions[]|select(.type=="Ready")|.status=="True"]|length>0 and all' "${out}/final-nodes.json" >/dev/null
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${out}/final-agent.json"
jq -e '[.items[]|select(.kind=="Pod")|.status.containerStatuses[]|.ready and .restartCount==0]|length>0 and all' "${out}/final-agent.json" >/dev/null
kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${out}/final-agent-spec.json"
cmp log/clock-final-20261109/original-spec.json "${out}/final-agent-spec.json"
cmp log/m10-runc-20261109/original-spec.json "${out}/final-agent-spec.json"
docker exec kno-dev uname -a >"${out}/host-kernel.txt"
docker exec kno-dev sha256sum /opt/kata/share/kata-containers/vmlinux-debug.container /opt/kata/share/kata-containers/config-6.18.56-208-debug >"${out}/guest-kernel-sha256.txt"
docker exec kno-dev grep CONFIG_TIME_NS /opt/kata/share/kata-containers/config-6.18.56-208-debug >"${out}/guest-time-ns-config.txt"
docker exec kno-dev crictl info | jq '{defaultRuntime:.config.containerd.defaultRuntimeName,runtimeTypes:(.config.containerd.runtimes|map_values(.runtimeType))}' >"${out}/runtime-types.json"
jq -e '.defaultRuntime=="runc"' "${out}/runtime-types.json" >/dev/null
docker exec kno-dev crictl pods -o json | jq '[.items[]|select(.state=="SANDBOX_READY" and (.runtimeHandler|startswith("kata")))]' >"${out}/final-kata-sandboxes.json"
jq -e 'length==0' "${out}/final-kata-sandboxes.json" >/dev/null
printf '{"exitCode":0,"originalSpecRestored":true,"testResourcesAbsent":true,"nodeReady":true,"tdkUntouched":true}\n' >"${out}/final-state.json"
