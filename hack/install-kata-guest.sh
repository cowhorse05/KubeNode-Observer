#!/usr/bin/env bash
# Opt-in development handler. Reuses the installed official debug kernel;
# leaves kata-qemu and runc defaults untouched. Refuses overwrite.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
export KUBE_CONTEXT MINIKUBE_PROFILE
kube() { kubectl --context "${KUBE_CONTEXT}" "$@"; }
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
bash hack/kata-preflight.sh
for path in /etc/containerd/config.toml.kno-before-guest /etc/containerd/kno-guest.toml /opt/kata/share/defaults/kata-containers/configuration-kno-guest.toml; do
  if node test -e "${path}"; then echo "refusing existing ${path}" >&2; exit 1; fi
done
[[ -z "$(kube get runtimeclass kno-kata-qemu-guest --ignore-not-found -o name)" ]]
[[ "$(node crictl pods -o json | jq '[.items[]|select(.state=="SANDBOX_READY" and (.runtimeHandler|startswith("kata")))]|length')" == 0 ]]
node test -s /opt/kata/share/kata-containers/vmlinux-debug.container
node grep -qx 'CONFIG_DEBUG_INFO_BTF=y' /opt/kata/share/kata-containers/config-6.18.56-208-debug
node grep -qx 'CONFIG_BPF_EVENTS=y' /opt/kata/share/kata-containers/config-6.18.56-208-debug
node grep -Fxq 'imports = ["/etc/containerd/kno-kata.toml"]' /etc/containerd/config.toml
kube -n kube-system create cm kno-experiment-lock --from-literal=run=kata-guest-install
changed=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ "${code}" != 0 && "${changed}" == true ]]; then
    node cp -p /etc/containerd/config.toml.kno-before-guest /etc/containerd/config.toml
    node systemctl restart containerd || true
    echo 'Guest handler install failed; original containerd config restored; new files retained for inspection.' >&2
  fi
  kube -n kube-system delete cm kno-experiment-lock || code=1
  exit "${code}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
node cp -p /etc/containerd/config.toml /etc/containerd/config.toml.kno-before-guest
node cp /opt/kata/share/defaults/kata-containers/configuration-kno-qemu.toml /opt/kata/share/defaults/kata-containers/configuration-kno-guest.toml
node sed -i 's@^kernel = .*@kernel = "/opt/kata/share/kata-containers/vmlinux-debug.container"@' /opt/kata/share/defaults/kata-containers/configuration-kno-guest.toml
docker cp deploy/kata/guest-runtime.toml "${MINIKUBE_PROFILE}:/etc/containerd/kno-guest.toml"
changed=true
node sed -i 's@^imports = .*@imports = ["/etc/containerd/kno-kata.toml", "/etc/containerd/kno-guest.toml"]@' /etc/containerd/config.toml
node containerd config dump >/dev/null
node systemctl restart containerd
kube wait --for=condition=Ready node --all --timeout=90s
node crictl info | jq -e '.config.containerd.defaultRuntimeName=="runc" and .config.containerd.runtimes["kata-qemu"].runtimeType=="io.containerd.kata.v2" and .config.containerd.runtimes["kata-qemu-guest"].runtimeType=="io.containerd.kata.v2"'
kube create -f deploy/kata/guest-runtimeclass.yaml
echo 'Installed dedicated debug-kernel Guest handler; existing kata-qemu/runc preserved.'
