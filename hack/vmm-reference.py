#!/usr/bin/env python3
"""External procfs reference, not a Guest metric or an API implementation."""
import json
import pathlib
import sys
import time

pid = int(sys.argv[1])
base = pathlib.Path(f"/proc/{pid}")
threads = []
for task in sorted((base / "task").iterdir(), key=lambda p: int(p.name)):
    stat = (task / "stat").read_text()
    fields = stat[stat.rfind(")") + 1:].split()
    schedstat = (task / "schedstat").read_text().split()
    threads.append({"tid": int(task.name), "comm": (task / "comm").read_text().strip(),
                    "startTimeTicks": int(fields[19]), "runtimeNs": int(schedstat[0]),
                    "waitNs": int(schedstat[1])})
print(json.dumps({"atUnixNs": time.time_ns(), "pid": pid, "threads": threads,
                  "schedstatsEnabled": pathlib.Path("/proc/sys/kernel/sched_schedstats").read_text().strip()}))
