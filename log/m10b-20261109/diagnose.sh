#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
out=log/m10b-20261109
kube(){ kubectl --context kno-dev "$@"; }
[[ -z "$(kube get ns kno-clockdomain-test --ignore-not-found -o name)" ]]
kube -n kube-system create cm kno-experiment-lock --from-literal=run=clockdomain-diagnostic
owned=false
cleanup(){ code=$?;trap - EXIT;if [[ "${owned}" == true ]];then kube delete ns kno-clockdomain-test --wait=true --timeout=90s || code=1;fi;kube -n kube-system delete cm kno-experiment-lock || code=1;printf '{"exitCode":%s}\n' "${code}" >"${out}/diagnose-exit.json";exit "${code}"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kube create ns kno-clockdomain-test;owned=true
for runtime in runc kata;do
  jq -n --arg runtime "${runtime}" '{apiVersion:"v1",kind:"Pod",metadata:{name:("clockcheck-"+$runtime),namespace:"kno-clockdomain-test"},spec:({automountServiceAccountToken:false,restartPolicy:"Never",containers:[{name:"check",image:"kubenode-observer/clockcheck:dev",imagePullPolicy:"Never",args:["--samples=50","--interval=100ms"],securityContext:{privileged:true},resources:{requests:{cpu:"100m",memory:"32Mi"},limits:{cpu:"500m",memory:"128Mi"}}}]} + (if $runtime=="kata" then {runtimeClassName:"kno-kata-qemu-guest"} else {} end))}' >"${out}/${runtime}-clock-pod-manifest.json"
  kube apply -f "${out}/${runtime}-clock-pod-manifest.json"
  phase=""
  for _ in $(seq 1 150);do phase="$(kube -n kno-clockdomain-test get pod "clockcheck-${runtime}" -o jsonpath='{.status.phase}')";if [[ "${phase}" == Succeeded || "${phase}" == Failed ]];then break;fi;sleep 1;done
  kube -n kno-clockdomain-test get pod "clockcheck-${runtime}" -o json >"${out}/${runtime}-clock-pod.json"
  kube -n kno-clockdomain-test logs "clockcheck-${runtime}" >"${out}/${runtime}-clock.json"
  [[ "${phase}" == Succeeded ]]
  jq -e '(.samples|length)==50' "${out}/${runtime}-clock.json" >/dev/null
  kube -n kno-clockdomain-test delete pod "clockcheck-${runtime}" --wait=true --timeout=90s
done
