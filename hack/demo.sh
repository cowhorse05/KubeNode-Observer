#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-kno-dev}"
KUBE_CONTEXT="${KUBE_CONTEXT:-${MINIKUBE_PROFILE}}"

bash hack/install-minikube.sh
bash hack/integration-test.sh

cat <<EOF

Demo completed successfully.

Open another terminal and run:
  kubectl --context ${KUBE_CONTEXT} -n kube-system port-forward service/kno-agent 18080:18080

Then inspect:
  ./bin/kno health
  curl http://127.0.0.1:18080/metrics
EOF
