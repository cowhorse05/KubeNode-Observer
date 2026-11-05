#!/usr/bin/env bash
set -euo pipefail
cd /mnt/d/Users/li.yufeng/Desktop/code/KubeNode-Observer
out=log/m8-20261103
kube() { kubectl --context kno-dev "$@"; }
node() { docker exec kno-dev "$@"; }
kube get node kno-dev -o json >"${out}/final-node.json"
kube -n kube-system get ds,pods -l app.kubernetes.io/name=kno-agent -o json >"${out}/final-agent.json"
kube -n kube-system get ds kno-agent -o json | jq -S .spec >"${out}/final-agent-spec.json"
jq -S .spec log/m8-final-20261103/original-agent.json >"${out}/original-agent-spec.json"
cmp "${out}/original-agent-spec.json" "${out}/final-agent-spec.json"
jq -e '[.status.conditions[]|select(.type=="Ready")][0].status=="True"' "${out}/final-node.json"
jq -e '([.items[]|select(.kind=="Pod")][0]) as $p | $p.status.containerStatuses[0].restartCount==0 and $p.status.containerStatuses[0].ready and ([.items[]|select(.kind=="DaemonSet")][0].spec.template.spec.containers[0].image=="kubenode-observer/kno-agent:dev")' "${out}/final-agent.json"
[[ -z "$(kube get ns kno-kata-test --ignore-not-found -o name)" ]]
[[ -z "$(kube -n kube-system get cm kno-experiment-lock --ignore-not-found -o name)" ]]
for pod in kno-exec-test-local kno-cpu-hog-local kno-runqlat-baseline kno-runqlat-contention kno-process-churn-local kno-observer-experiment; do
  [[ -z "$(kube get pod "${pod}" --ignore-not-found -o name)" ]]
done
node curl -fsS http://127.0.0.1:8090/sandboxes >"${out}/final-monitor.txt"
[[ ! -s "${out}/final-monitor.txt" ]]
node crictl pods -o json >"${out}/final-cri-pods.json"
jq -e '[.items[]|select(.runtimeHandler=="kata-qemu" and .state=="SANDBOX_READY")]|length==0' "${out}/final-cri-pods.json"
node systemctl is-active kno-kata-monitor kno-kata-shm >"${out}/final-services.txt"
kube get runtimeclass kno-kata-qemu -o json >"${out}/final-runtimeclass.json"
node containerd config dump >"${out}/final-containerd.txt"
uname -a
go version
minikube version
docker inspect kno-dev --format '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidMode}} {{.HostConfig.CgroupnsMode}}'
node df -B1 /dev/shm
node journalctl -u kno-kata-monitor --since '2026-11-03 09:07:00 UTC' --until '2026-11-03 09:26:00 UTC' --no-pager >log/m8-pilot-20261103/failure-monitor-late-capture.txt
bash -n hack/kata-test.sh hack/kata-vmm-test.sh log/m8-runc-20261103/run.sh
python3 hack/validate-host-contract.py log/m8-final-20261103
git diff --check
echo 'Final state verified: original Agent spec, Ready node, zero restarts, no test namespace/Pods/lock, empty Monitor; Kata runtime/services retained.'
