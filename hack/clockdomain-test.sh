#!/usr/bin/env bash
# Explicit privileged diagnostic. Queries adjtimex with Modes=0; never sets clocks.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
CLOCKDOMAIN_OUTPUT="${CLOCKDOMAIN_OUTPUT:-log/clockdomain-test-$(date -u +%Y%m%dT%H%M%SZ)}"
CLOCKCHECK_IMAGE="${CLOCKCHECK_IMAGE:-kubenode-observer/clockcheck:dev}"
export CLOCKCHECK_IMAGE
kube(){ kubectl --context "${KUBE_CONTEXT}" "$@"; }
[[ -z "$(kube get ns kno-clockdomain-test --ignore-not-found -o name)" ]]
kube get runtimeclass kno-kata-qemu-guest >/dev/null
mkdir -- "${CLOCKDOMAIN_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${CLOCKDOMAIN_OUTPUT}"
owned=false
cleanup(){
  code=$?;trap - EXIT
  if [[ "${owned}" == true ]];then kube delete ns kno-clockdomain-test --wait=true --timeout=90s || code=1;fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${CLOCKDOMAIN_OUTPUT}/exit.json";exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
git rev-parse HEAD >"${CLOCKDOMAIN_OUTPUT}/source-head.txt"
sha256sum internal/clockdomain/*.go cmd/kno-clockcheck/*.go bin/kno-clockcheck >"${CLOCKDOMAIN_OUTPUT}/source-sha256.txt"
docker image inspect "${CLOCKCHECK_IMAGE}" >"${CLOCKDOMAIN_OUTPUT}/images.json"
# Only the child unshare namespace gets an offset; initial Host time is untouched.
sudo -n ./bin/kno-clockcheck --samples=20 >"${CLOCKDOMAIN_OUTPUT}/host-clock.json"
sudo -n unshare --time --monotonic 2 --fork ./bin/kno-clockcheck --samples=5 >"${CLOCKDOMAIN_OUTPUT}/namespace-positive.json"
python3 hack/validate-clockdomain.py "${CLOCKDOMAIN_OUTPUT}/host-clock.json" --expect-offset-ns=0 >"${CLOCKDOMAIN_OUTPUT}/host-validation.json"
python3 hack/validate-clockdomain.py "${CLOCKDOMAIN_OUTPUT}/namespace-positive.json" --expect-offset-ns=2000000000 >"${CLOCKDOMAIN_OUTPUT}/namespace-validation.json"
kube create ns kno-clockdomain-test;owned=true
for runtime in runc kata;do
  jq -n --arg runtime "${runtime}" '{apiVersion:"v1",kind:"Pod",metadata:{name:("clockcheck-"+$runtime),namespace:"kno-clockdomain-test"},spec:({automountServiceAccountToken:false,restartPolicy:"Never",containers:[{name:"check",image:env.CLOCKCHECK_IMAGE,imagePullPolicy:"Never",args:["--samples=50","--interval=100ms"],securityContext:{privileged:true},resources:{requests:{cpu:"100m",memory:"32Mi"},limits:{cpu:"500m",memory:"128Mi"}}}]} + (if $runtime=="kata" then {runtimeClassName:"kno-kata-qemu-guest"} else {} end))}' >"${CLOCKDOMAIN_OUTPUT}/${runtime}-manifest.json"
  kube apply -f "${CLOCKDOMAIN_OUTPUT}/${runtime}-manifest.json"
  phase=""
  for _ in $(seq 1 150);do phase="$(kube -n kno-clockdomain-test get pod "clockcheck-${runtime}" -o jsonpath='{.status.phase}')";if [[ "${phase}" == Succeeded || "${phase}" == Failed ]];then break;fi;sleep 1;done
  kube -n kno-clockdomain-test get pod "clockcheck-${runtime}" -o json >"${CLOCKDOMAIN_OUTPUT}/${runtime}-pod.json"
  kube -n kno-clockdomain-test logs "clockcheck-${runtime}" >"${CLOCKDOMAIN_OUTPUT}/${runtime}-clock.json"
  [[ "${phase}" == Succeeded ]]
  python3 hack/validate-clockdomain.py "${CLOCKDOMAIN_OUTPUT}/${runtime}-clock.json" --expect-offset-ns=0 >"${CLOCKDOMAIN_OUTPUT}/${runtime}-validation.json"
  kube -n kno-clockdomain-test delete pod "clockcheck-${runtime}" --wait=true --timeout=90s
done
echo 'BPF/user brackets and +2s child namespace control passed; no future drift bound inferred.'
