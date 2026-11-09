#!/usr/bin/env bash
# Compatibility check only; BPF sources/maps are unchanged, no new cost claim.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
out=log/m10-runc-20261109
kube(){ kubectl --context kno-dev "$@"; }
for pod in kno-exec-test-local kno-cpu-hog-local kno-runqlat-baseline kno-runqlat-contention;do [[ -z "$(kube get pod "${pod}" --ignore-not-found -o name)" ]];done
[[ ! -e "${out}/original.json" ]]
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${out}"
restore=false;forward=""
cleanup(){
  code=$?;trap - EXIT
  if [[ -n "${forward}" ]];then kill "${forward}" 2>/dev/null || true;wait "${forward}" 2>/dev/null || true;fi
  if [[ "${restore}" == true ]];then
    kube apply -f "${out}/original-manifest.json" || code=1
    kube -n kube-system rollout status ds/kno-agent --timeout=90s || code=1
    kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${out}/restored-spec.json" || code=1
    jq -S .spec "${out}/original.json" >"${out}/original-spec.json"
    cmp "${out}/original-spec.json" "${out}/restored-spec.json" || code=1
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${out}/exit.json";exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kube -n kube-system get ds kno-agent -o json >"${out}/original.json"
jq 'del(.metadata.managedFields,.metadata.resourceVersion,.metadata.uid,.metadata.creationTimestamp,.metadata.generation,.status)' "${out}/original.json" >"${out}/original-manifest.json"
jq '.spec.template.spec.containers[0].image="kubenode-observer/kno-agent:clock"' "${out}/original-manifest.json" >"${out}/candidate.json"
restore=true;kube apply -f "${out}/candidate.json";kube -n kube-system rollout status ds/kno-agent --timeout=90s
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${out}/agent-before.json"
kube -n kube-system port-forward --address=127.0.0.1 ds/kno-agent :18080 >"${out}/forward.txt" 2>&1 & forward=$!
endpoint=""
for _ in $(seq 1 100);do address="$(awk '/Forwarding from 127.0.0.1:/ {print $3;exit}' "${out}/forward.txt")";if [[ -n "${address}" ]];then endpoint="http://${address}";break;fi;sleep .1;done
[[ -n "${endpoint}" ]]
curl -fsS "${endpoint}/metrics" >"${out}/metrics-before.txt"
bash hack/integration-test.sh 2>&1 | tee "${out}/integration.txt"
sleep 16
curl -fsS "${endpoint}/metrics" >"${out}/metrics-after.txt"
kube -n kube-system get pods -l app.kubernetes.io/name=kno-agent -o json >"${out}/agent-after.json"
jq -e --slurpfile b "${out}/agent-before.json" '.items[0].metadata.uid==$b[0].items[0].metadata.uid and .items[0].status.containerStatuses[0].restartCount==0' "${out}/agent-after.json" >/dev/null
awk '/^kno_agent_events_lost_total / {if ($2!=0) bad=1; found=1} /^kno_agent_bpf_map_stats_ready\{/ {if ($2!=1) bad=1; ready++} /^kno_agent_bpf_map_stats_refresh_errors_total\{/ {if ($2!=0) bad=1; sources++} END {if (bad || !found || ready!=3 || sources!=3) exit 1}' "${out}/metrics-after.txt"
echo 'M10a final-image runc compatibility checks passed.'
