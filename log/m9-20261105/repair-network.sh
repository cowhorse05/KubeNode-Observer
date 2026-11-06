#!/usr/bin/env bash
# Recorded environment repair, not a generic installation step.
set -euo pipefail
cd /mnt/d/Users/li.yufeng/Desktop/code/KubeNode-Observer
kube() { kubectl --context kno-dev "$@"; }
kube -n kube-system create cm kno-experiment-lock --from-literal=run=m9-network-repair
trap 'kubectl --context kno-dev -n kube-system delete cm kno-experiment-lock' EXIT
docker exec kno-dev cat /etc/hosts | cmp - log/m9-20261105/node-hosts.before
if ! docker exec kno-dev test -e /etc/hosts.kno-before-m9-network;then docker exec kno-dev cp -p /etc/hosts /etc/hosts.kno-before-m9-network;fi
docker exec kno-dev cat /etc/hosts.kno-before-m9-network | cmp - log/m9-20261105/node-hosts.before
docker cp log/m9-20261105/node-hosts.repaired kno-dev:/etc/kno-m9-hosts
docker exec kno-dev cp /etc/kno-m9-hosts /etc/hosts
docker exec kno-dev getent hosts control-plane.minikube.internal
for attempt in $(seq 1 45);do
  if docker exec kno-dev curl -kfsS --connect-timeout 1 --max-time 2 https://10.96.0.1:443/healthz;then
    docker exec kno-dev iptables-save -t nat >log/m9-20261105/nat-after.txt
    docker exec kno-dev cat /etc/hosts >log/m9-20261105/node-hosts.after
    echo 'Service VIP restored without changing Kubernetes/CRI identity validation.'
    exit 0
  fi
  sleep 1
done
echo 'Service VIP still unavailable; hosts backup retained, inspect kube-proxy.' >&2
exit 1
