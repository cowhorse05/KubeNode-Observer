#!/usr/bin/env bash
set -euo pipefail

KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
TEST_POD="kno-exec-test-local"

kubectl --context "${KUBE_CONTEXT}" delete pod "${TEST_POD}" --ignore-not-found
kubectl --context "${KUBE_CONTEXT}" apply -f examples/exec-test/pod-local.yaml
kubectl --context "${KUBE_CONTEXT}" wait \
  --for=jsonpath='{.status.phase}'=Succeeded \
  "pod/${TEST_POD}" \
  --timeout=60s

for _ in $(seq 1 10); do
  if kubectl --context "${KUBE_CONTEXT}" -n kube-system logs \
    -l app.kubernetes.io/name=kno-agent --tail=200 | grep -q '"comm":"workload"'; then
    echo "smoke test passed: workload exec event observed"
    exit 0
  fi
  sleep 1
done

echo "smoke test failed: workload exec event was not observed" >&2
exit 1
