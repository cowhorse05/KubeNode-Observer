#!/usr/bin/env python3
"""Offline contract validation of saved real HTTP/CLI captures. No network."""
import glob
import json
from pathlib import Path
import sys

import jsonschema
import yaml

root = Path(__file__).resolve().parent.parent
spec = yaml.safe_load((root / "api/host-resources.openapi.yaml").read_text())
assert spec["openapi"] == "3.1.0"
schema = {"$ref": "#/components/schemas/HostResponse", "components": spec["components"]}
jsonschema.Draft202012Validator.check_schema(schema)
validator = jsonschema.Draft202012Validator(schema)
error_validator = jsonschema.Draft202012Validator({
    "$ref": "#/components/schemas/Error", "components": spec["components"]})
directory = Path(sys.argv[1])
count = 0
for pattern in ("m8-warmup-*.json", "m8-baseline-[12].json", "m8-contention-[12].json",
                "m8-recovery-[12].json", "m8-concurrent-first.json", "m8-after-delete.json"):
    for filename in sorted(glob.glob(str(directory / pattern))):
        validator.validate(json.loads(Path(filename).read_text()))
        count += 1
assert count >= 9, f"incomplete live captures: {count}"
for name, code in (("invalid", "invalid_query"), ("busy", "measurement_busy")):
    value = json.loads((directory / f"m8-{name}.json").read_text())
    error_validator.validate(value)
    assert value["code"] == code
print(json.dumps({"responseCapturesValidated": count, "errorCapturesValidated": 2,
                  "schema": "OpenAPI 3.1 / JSON Schema 2020-12", "result": "passed"}))
