#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
export CLOCK_GUEST_IMAGE=kubenode-observer/guest-probe:clock
kubectl --context kno-dev create --dry-run=client -f examples/guest-probe/pods.yaml -o json | jq -s '{apiVersion:"v1",kind:"List",items:[.[]|select(.metadata.name=="kno-guest")|.metadata.namespace="kno-clock-test"|(.spec.containers[].image)|=if .=="kubenode-observer/guest-probe:dev" then env.CLOCK_GUEST_IMAGE else . end]}' >log/m10-20261109/pod-preview.json
jq -e '(.items|length)==1 and .items[0].metadata.namespace=="kno-clock-test" and .items[0].spec.containers[0].image=="kubenode-observer/guest-probe:clock"' log/m10-20261109/pod-preview.json
python3 - <<'PY'
import jsonschema, yaml
with open('api/guest-clock.openapi.yaml') as stream:
    spec=yaml.safe_load(stream)
for name in spec['components']['schemas']:
    jsonschema.Draft202012Validator.check_schema({'$ref':'#/components/schemas/'+name,'components':spec['components']})
print('Clock schemas parsed and validated.')
PY
