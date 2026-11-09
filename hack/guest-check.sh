#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
GUEST_OUTPUT="${GUEST_OUTPUT:-log/guest-check-$(date -u +%Y%m%dT%H%M%SZ)}"
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
[[ -z "$(kube get ns kno-guest-test --ignore-not-found -o name)" ]]
mkdir -- "${GUEST_OUTPUT}"
kube -n kube-system create cm kno-experiment-lock --from-literal=run="${GUEST_OUTPUT}"
owned=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ "${owned}" == true ]]; then kube delete ns kno-guest-test --wait=true --timeout=90s || code=1; fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  printf '{"exitCode":%s}\n' "${code}" >"${GUEST_OUTPUT}/exit.json"
  exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
kube create ns kno-guest-test
owned=true
variants=(default unprivileged debug)
if [[ "${GUEST_CHECK_MODE:-capability}" == kernel ]];then variants=(kernel);fi
for variant in "${variants[@]}"; do
  kube create --dry-run=client -f examples/guest-probe/check.yaml -o json | jq --arg variant "${variant}" 'if $variant=="default" then .spec.runtimeClassName="kno-kata-qemu" elif $variant=="unprivileged" then .spec.containers[0].securityContext.privileged=false else . end' >"${GUEST_OUTPUT}/${variant}-manifest.json"
  if [[ "${variant}" == kernel ]];then
    jq '.spec.containers[0].image="kubenode-observer/guest-kernel-test:dev" | .spec.containers[0].args=["-test.run=TestCPU|TestMapStats|TestRunQLatTaskIdentityKernel","-test.v"] | .spec.containers[0].env=[{name:"KNO_BPF_TEST",value:"1"}]' "${GUEST_OUTPUT}/${variant}-manifest.json" >"${GUEST_OUTPUT}/kernel-test-manifest.json"
    cp "${GUEST_OUTPUT}/kernel-test-manifest.json" "${GUEST_OUTPUT}/${variant}-manifest.json"
  fi
  kube apply -f "${GUEST_OUTPUT}/${variant}-manifest.json"
  phase=""
  for _ in $(seq 1 180); do
    phase="$(kube -n kno-guest-test get pod kno-guest-check -o jsonpath='{.status.phase}')"
    if [[ "${phase}" == Succeeded || "${phase}" == Failed ]]; then break; fi
    sleep 1
  done
  kube -n kno-guest-test get pod kno-guest-check -o json >"${GUEST_OUTPUT}/${variant}-pod.json"
  kube -n kno-guest-test logs kno-guest-check >"${GUEST_OUTPUT}/${variant}.json"
  if [[ "${variant}" == kernel ]];then
    mv "${GUEST_OUTPUT}/${variant}.json" "${GUEST_OUTPUT}/${variant}.txt"
    [[ "${phase}" == Succeeded ]]
    grep -qx PASS "${GUEST_OUTPUT}/${variant}.txt"
  elif [[ "${variant}" == debug ]]; then
    [[ "${phase}" == Succeeded ]]
    jq -e '.domain=="guest" and .ready and .btfBytes>0' "${GUEST_OUTPUT}/${variant}.json"
  else
    [[ "${phase}" == Failed ]]
    jq -e '.ready==false and (.error|length)>0' "${GUEST_OUTPUT}/${variant}.json"
  fi
  kube -n kno-guest-test delete pod kno-guest-check --wait=true --timeout=90s
done
echo "Guest ${GUEST_CHECK_MODE:-capability} checks passed."
