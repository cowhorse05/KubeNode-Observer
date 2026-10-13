#!/usr/bin/env bash
set -euo pipefail

failed=0

check_command() {
  if command -v "$1" >/dev/null 2>&1; then
    printf '[ok]   %s: %s\n' "$1" "$(command -v "$1")"
  else
    printf '[fail] %s is missing\n' "$1"
    failed=1
  fi
}

printf '%s\n' 'KubeNode-Observer environment check'
printf 'kernel: %s\n' "$(uname -r)"

for command in go clang make docker kubectl minikube; do
  check_command "$command"
done

if [[ -r /sys/kernel/btf/vmlinux ]]; then
  printf '%s\n' '[ok]   kernel BTF is available'
else
  printf '%s\n' '[fail] /sys/kernel/btf/vmlinux is unavailable'
  failed=1
fi

if [[ -f /sys/fs/cgroup/cgroup.controllers ]]; then
  printf '%s\n' '[ok]   cgroup v2 is active'
else
  printf '%s\n' '[fail] cgroup v2 is not active'
  failed=1
fi

if [[ -f /usr/include/bpf/bpf_helpers.h ]]; then
  printf '%s\n' '[ok]   libbpf headers are installed'
else
  printf '%s\n' '[fail] install libbpf-dev to provide bpf_helpers.h'
  failed=1
fi

exit "$failed"

