#!/usr/bin/env python3
"""Offline live-capture/schema + exact integer arithmetic checks, no network."""
import json
from pathlib import Path
import sys

import jsonschema
import yaml

root = Path(__file__).resolve().parent.parent
spec = yaml.safe_load((root / "api/guest-clock.openapi.yaml").read_text())
validators = {}
for name in spec["components"]["schemas"]:
    schema = {"$ref": f"#/components/schemas/{name}", "components": spec["components"]}
    jsonschema.Draft202012Validator.check_schema(schema)
    validators[name] = jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker())
validators["Error"].validate(spec["paths"]["/api/v1/guest/clock"]["get"]["responses"]["400"]["content"]["application/json"]["example"])
directory = Path(sys.argv[1])
files = sorted(directory.glob("idle-*.json")) + sorted(directory.glob("loaded-*.json")) + [directory / "recreated.json"]
assert len(files) == 11
summary = []
for path in files:
    value = json.loads(path.read_text())
    validators["Calibration"].validate(value)
    widths = []
    previous = None
    nonces = set()
    for sample in value["samples"]:
        assert sample["requestId"] not in nonces
        nonces.add(sample["requestId"])
        h1, g2, g3, h4 = [int(sample[name]) for name in ("hostSendNs", "guestReceiveNs", "guestSendNs", "hostReceiveNs")]
        lower, upper, midpoint = [int(sample[name]) for name in ("offsetLowerNs", "offsetUpperNs", "offsetMidpointNs")]
        assert all(0 < n <= 2**63-1 for n in (h1, g2, g3, h4))
        assert h4 >= h1 and g3 >= g2 and h4-h1 >= g3-g2
        assert lower == g3-h4 and upper == g2-h1
        assert lower <= upper and midpoint == lower+(upper-lower)//2
        if previous:
            assert h1 >= previous[0] and g2 >= previous[1]
        previous = h4, g3
        widths.append(upper-lower)
    selected = value["selectedSample"]
    assert selected == widths.index(min(widths)), "selection must be first minimum residual width"
    summary.append({"file": path.name, "samples": len(widths), "selectedWidthNs": widths[selected],
                    "selectedMidpointNs": value["samples"][selected]["offsetMidpointNs"]})
direct = json.loads((directory / "direct.json").read_text())
validators["ClockReply"].validate(direct)
assert int(direct["sentNs"]) >= int(direct["receivedNs"]) > 0
for name in ("deleted.json", "disabled.json"):
    validators["Error"].validate(json.loads((directory / name).read_text()))
print(json.dumps({"result": "passed", "calibrationsValidated": len(files), "directRepliesValidated": 1,
                  "errorResponsesValidated": 2, "summaries": summary}, indent=2))
