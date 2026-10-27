#!/usr/bin/env bash
# An opt-in, pinned offline installation for our existing Docker-driver node.
# Not a general replacement for upstream kata-deploy/Helm.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
KUBE_CONTEXT="${KUBE_CONTEXT:-kno-dev}"
MINIKUBE_PROFILE="${MINIKUBE_PROFILE:-${KUBE_CONTEXT}}"
KATA_ARCHIVE="${KATA_ARCHIVE:-dist/kata-4.3.1/kata-go-static-4.3.1-amd64.tar.zst}"
export KUBE_CONTEXT MINIKUBE_PROFILE
bash hack/kata-preflight.sh
[[ "$(uname -m)" == x86_64 ]]
echo '66b35b06f6b41b56f8606ddf3ed3e5660b0971d1e309d903457c4056a0b9eff0  '"${KATA_ARCHIVE}" | sha256sum -c -
node() { docker exec "${MINIKUBE_PROFILE}" "$@"; }
configured=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ "${code}" != 0 && "${configured}" == true ]]; then
    node cp -p /etc/containerd/config.toml.kno-before-kata /etc/containerd/config.toml
    node systemctl disable --now kno-kata-monitor.service kno-kata-shm.service || true
    node systemctl restart containerd
    echo "installation failed; original containerd config restored; /opt/kata retained for inspection" >&2
  fi
  kubectl --context "${KUBE_CONTEXT}" -n kube-system delete cm kno-experiment-lock || code=1
  exit "${code}"
}
kubectl --context "${KUBE_CONTEXT}" -n kube-system create cm kno-experiment-lock --from-literal=run=kata-install
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
for managed_path in /opt/kata /etc/containerd/config.toml.kno-before-kata /etc/containerd/kno-kata.toml; do
  if node test -e "${managed_path}"; then
    echo "refusing to overwrite existing ${managed_path}; inspect the prior installation/backup" >&2
    exit 1
  fi
done
if kubectl --context "${KUBE_CONTEXT}" get runtimeclass kno-kata-qemu >/dev/null 2>&1; then echo "RuntimeClass already exists" >&2; exit 1; fi
node grep -qx 'version = 2' /etc/containerd/config.toml
node grep -qx '# imports' /etc/containerd/config.toml
if node grep -Eq '^imports[[:space:]]*=|runtimes.kata' /etc/containerd/config.toml; then echo "unexpected existing imports/Kata runtime; inspect manually" >&2; exit 1; fi
# Validate archive destinations before extracting into the node. A pinned hash
# is mandatory; reject unexpected root paths as an additional guard.
if tar --zstd -tf "${KATA_ARCHIVE}" | grep -Ev '^\./$|^\./opt/$|^\./opt/kata(/|$)' | grep .; then echo "unexpected archive member" >&2; exit 1; fi
zstd -dc "${KATA_ARCHIVE}" | docker exec -i "${MINIKUBE_PROFILE}" tar --no-same-owner -xf - -C /
node /opt/kata/bin/kata-runtime --version
node /opt/kata/bin/kata-runtime check
# virtio-fs backs Guest RAM with this node-private tmpfs. Docker defaults to
# 64 MiB, causing KVM EFAULT when the 512 MiB guest faults in memory. This raises
# capacity only (not a reservation), not the Agent's cgroup memory limit.
node mount -o remount,size=1G /dev/shm
node cp -p /etc/containerd/config.toml /etc/containerd/config.toml.kno-before-kata
node cp /opt/kata/share/defaults/kata-containers/configuration-qemu.toml /opt/kata/share/defaults/kata-containers/configuration-kno-qemu.toml
node sed -i -e 's/^default_memory = .*/default_memory = 512/' -e 's/^default_maxmemory = .*/default_maxmemory = 2048/' -e 's/^default_maxvcpus = .*/default_maxvcpus = 2/' /opt/kata/share/defaults/kata-containers/configuration-kno-qemu.toml
docker cp deploy/kata/containerd-runtime.toml "${MINIKUBE_PROFILE}:/etc/containerd/kno-kata.toml"
docker cp deploy/kata/kno-kata-monitor.service "${MINIKUBE_PROFILE}:/etc/systemd/system/kno-kata-monitor.service"
docker cp deploy/kata/kno-kata-shm.service "${MINIKUBE_PROFILE}:/etc/systemd/system/kno-kata-shm.service"
configured=true
node sed -i '/^# imports$/a imports = ["/etc/containerd/kno-kata.toml"]' /etc/containerd/config.toml
node containerd config dump >/dev/null
node systemctl daemon-reload
node systemctl enable --now kno-kata-shm.service
node systemctl restart containerd
node systemctl enable --now kno-kata-monitor.service
kubectl --context "${KUBE_CONTEXT}" wait --for=condition=Ready node --all --timeout=90s
node crictl info | jq -e '.config.containerd.defaultRuntimeName == "runc" and .config.containerd.runtimes["kata-qemu"].runtimeType == "io.containerd.kata.v2"'
kubectl --context "${KUBE_CONTEXT}" create -f deploy/kata/runtimeclass.yaml
echo 'Kata 4.3.1 Go/QEMU installed; default runtime remains runc. Monitor is loopback-only.'
