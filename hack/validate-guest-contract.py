#!/usr/bin/env python3
"""Validate saved live Guest captures offline. Requires PyYAML/jsonschema."""
import json
from pathlib import Path
import sys

import jsonschema
import yaml

root = Path(__file__).resolve().parent.parent
spec = yaml.safe_load((root / "api/guest-resources.openapi.yaml").read_text())
assert spec["openapi"] == "3.1.0"
validators = {}
for name in spec["components"]["schemas"]:
    schema = {"$ref": f"#/components/schemas/{name}", "components": spec["components"]}
    jsonschema.Draft202012Validator.check_schema(schema)
    validators[name] = jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker())
for response in spec["paths"]["/api/v1/guest/resources"]["get"]["responses"].values():
    media = response["content"]["application/json"]
    if "example" in media:
        validators["Error"].validate(media["example"])

directory = Path(sys.argv[1])
files = {directory / "direct-probe.json"}
for phase in ("baseline", "contention", "recovery"):
    files.update(directory.glob(f"{phase}-*.json"))
files = sorted(path for path in files if not path.name.endswith("-host.json"))
assert len(files) >= 7, "incomplete live captures"
for path in files:
    value = json.loads(path.read_text())
    validators["Response"].validate(value)
    assert value["totalItems"] >= len(value["processes"])
    for process in value["processes"]:
        coverage = process["runqlatCoverage"]
        assert coverage["observedThreads"] <= coverage["totalThreads"]
        assert process["partial"] == bool(process.get("issues"))
        if process["runqlat"] is not None:
            assert coverage["observedThreads"] > 0
            assert sum(b["count"] for b in process["runqlat"].get("buckets", [])) == process["runqlat"]["count"]
        else:
            assert coverage["observedThreads"] == 0
        if coverage["observedThreads"] < coverage["totalThreads"]:
            assert process["partial"]
validators["Error"].validate(json.loads((directory / "after-delete.json").read_text()))
print(json.dumps({"responseCapturesValidated": len(files), "errorCapturesValidated": 1,
                  "schema": "OpenAPI 3.1 / JSON Schema 2020-12 + coverage invariants", "result": "passed"}))
