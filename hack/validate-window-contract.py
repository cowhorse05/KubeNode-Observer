#!/usr/bin/env python3
"""Offline schema, monotonic ordering and generation checks of live windows."""
import json
from pathlib import Path
import sys

import jsonschema
import yaml

root = Path(__file__).resolve().parent.parent
window_uri = (root / "api/window-timing.schema.json").as_uri()
window_schema = json.loads((root / "api/window-timing.schema.json").read_text())


def reject_remote(uri):
    raise ValueError(f"unregistered schema URI; offline validation only: {uri}")


def validator(filename, schema_name):
    spec = yaml.safe_load((root / "api" / filename).read_text())
    schema = {"$id": (root / "api" / filename).as_uri(), "$ref": f"#/components/schemas/{schema_name}", "components": spec["components"]}
    jsonschema.Draft202012Validator.check_schema(schema)
    # RefResolver supports the repository's installed jsonschema 4.10.3 as well
    # as newer releases. All external references must be in this local store.
    resolver = jsonschema.RefResolver(base_uri=schema["$id"], referrer=schema,
                                      store={window_uri: window_schema},
                                      handlers={scheme: reject_remote for scheme in ("file", "http", "https")})
    return jsonschema.Draft202012Validator(schema, resolver=resolver, format_checker=jsonschema.FormatChecker())


directory = Path(sys.argv[1])
guest = validator("guest-resources.openapi.yaml", "Response")
host = validator("host-resources.openapi.yaml", "HostResponse")
calibration = validator("guest-clock.openapi.yaml", "Calibration")
read = lambda name: json.loads((directory / name).read_text())
for name, contract in (("guest-default.json", guest), ("host-default.json", host), ("host-empty.json", host)):
    value = read(name)
    contract.validate(value)
    assert "timing" not in value, name
assert read("host-empty.json")["items"] == []
summary = []
for round in range(1, 4):
    g, h, pre, post = [read(f"{name}-{round}.json") for name in ("guest", "host", "pre", "post")]
    guest.validate(g); host.validate(h); calibration.validate(pre); calibration.validate(post)
    assert pre["host"] == post["host"] == h["timing"]["identity"]
    assert pre["guest"] == post["guest"] == g["timing"]["identity"]
    assert pre["probeInstanceId"] == post["probeInstanceId"] == g["instanceId"]
    assert pre["podUid"] == post["podUid"] == g["podUid"]
    assert pre["sandboxId"] == post["sandboxId"] == g["sandboxId"]
    assert g["bootId"] == g["timing"]["identity"]["bootId"]
    assert len(h["items"]) == 1 and h["items"][0]["podUid"] == g["podUid"] and h["items"][0]["sandboxId"] == g["sandboxId"]
    assert len(g["processes"]) >= 1 and any(p["cpu"] is not None for p in g["processes"])
    assert h["items"][0]["cpu"] is not None
    record = {"round": round}
    for domain, response in (("host", h), ("guest", g)):
        w = response["timing"]
        previous = 0
        widths = []
        for endpoint in ("start", "end"):
            for source in ("cpu", "runqlat"):
                span = w[endpoint][source]
                b, a = int(span["beforeNs"]), int(span["afterNs"])
                assert 0 < b <= a <= 2**63-1 and b >= previous
                previous = a
                widths.append(a-b)
        if domain == "host":
            assert int(pre["samples"][-1]["hostReceiveNs"]) <= int(w["start"]["cpu"]["beforeNs"])
            assert int(w["end"]["runqlat"]["afterNs"]) <= int(post["samples"][0]["hostSendNs"])
        else:
            assert int(pre["samples"][-1]["guestSendNs"]) <= int(w["start"]["cpu"]["beforeNs"])
            assert int(w["end"]["runqlat"]["afterNs"]) <= int(post["samples"][0]["guestReceiveNs"])
        record[domain] = {"readBracketWidthsNs": [str(n) for n in widths],
                          "cpuReadInteriorNs": str(int(w["end"]["cpu"]["beforeNs"])-int(w["start"]["cpu"]["afterNs"])),
                          "runqlatReadInteriorNs": str(int(w["end"]["runqlat"]["beforeNs"])-int(w["start"]["runqlat"]["afterNs"]))}
    summary.append(record)
print(json.dumps({"result": "passed", "resourceResponsesValidated": 9, "calibrationsValidated": 6,
                  "rounds": summary, "crossDomainOverlap": "not calculated; rate model still required"}, indent=2))
