#!/usr/bin/env bash
set -euo pipefail

profile="${MINIKUBE_PROFILE:-kno-dev}"
cpus="${MINIKUBE_CPUS:-2}"
memory="${MINIKUBE_MEMORY:-4096mb}"

minikube start \
  --profile "$profile" \
  --driver docker \
  --container-runtime containerd \
  --cni bridge \
  --cpus "$cpus" \
  --memory "$memory"

kubectl config use-context "$profile"

