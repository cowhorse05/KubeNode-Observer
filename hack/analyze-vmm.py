#!/usr/bin/env python3
"""Summarize saved M8 windows, without pretending reference bounds are equal."""
import json
from pathlib import Path
import sys

directory = Path(sys.argv[1])
rows = []
for phase in ("baseline", "contention", "recovery"):
    for round_number in (1, 2):
        prefix = f"m8-{phase}-{round_number}"
        data = json.loads((directory / f"{prefix}.json").read_text())
        vm, = [row for row in data["items"] if row["kind"] == "kata-sandbox"]
        before = json.loads((directory / f"{prefix}-reference-start.json").read_text())
        after = json.loads((directory / f"{prefix}-reference-end.json").read_text())
        old = {task["tid"]: task for task in before["threads"]}
        assert before["pid"] == after["pid"] == vm["pid"]
        assert len(old) == len(after["threads"])
        runtime = 0
        for task in after["threads"]:
            start = old[task["tid"]]
            assert start["startTimeTicks"] == task["startTimeTicks"]
            assert task["runtimeNs"] >= start["runtimeNs"]
            runtime += task["runtimeNs"] - start["runtimeNs"]
        reference_window = (after["atUnixNs"] - before["atUnixNs"]) / 1e9
        rows.append({"phase": phase, "round": round_number,
                     "ebpfCPUPercent": vm["cpu"]["percent"],
                     "ebpfWindowSeconds": vm["cpu"]["windowSeconds"],
                     "outerSchedstatCPUPercent": runtime / 1e9 / reference_window * 100,
                     "outerWindowSeconds": reference_window,
                     "schedstatsEnabled": after["schedstatsEnabled"],
                     "vcpuCount": sum(t["role"] == "vcpu" for t in vm["threads"]),
                     "waitCount": vm["vcpuRunqlat"]["count"],
                     "p95Us": vm["vcpuRunqlat"]["p95Us"],
                     "rssBytes": vm["memory"]["bytes"], "partial": vm["partial"]})
print(json.dumps({"windows": rows, "referenceLimit":
                  "Outer procfs window includes HTTP overhead, not equal-bound accuracy; "
                  "disabled schedstats waitNs is not a latency reference."}, indent=2))
