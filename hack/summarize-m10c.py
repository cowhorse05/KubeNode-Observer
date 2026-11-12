#!/usr/bin/env python3
"""Summarize M10c Host x Guest captures without turning trends into gates."""

import argparse
import json
import math
import re
import statistics
from pathlib import Path


CAPTURE = re.compile(r"^round-(\d+)-h([01])-g(1|8)-(host|guest)\.json$")
QUADRANTS = ((0, 1), (0, 8), (1, 1), (1, 8))


def number(value, field, *, positive=False, nonnegative=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        raise ValueError(f"{field} must be a finite number")
    if positive and value <= 0:
        raise ValueError(f"{field} must be positive")
    if nonnegative and value < 0:
        raise ValueError(f"{field} must be nonnegative")
    return value


def integer(value, field, *, positive=False, nonnegative=False):
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{field} must be an integer")
    if positive and value <= 0:
        raise ValueError(f"{field} must be positive")
    if nonnegative and value < 0:
        raise ValueError(f"{field} must be nonnegative")
    return value


def validate_timing(value, field):
    if not isinstance(value, dict) or value.get("clock") != "CLOCK_MONOTONIC":
        raise ValueError(f"{field} timing missing or uses the wrong clock")
    identity = value.get("identity")
    if (not isinstance(identity, dict)
            or not isinstance(identity.get("bootId"), str)
            or not re.fullmatch(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}", identity["bootId"])
            or not isinstance(identity.get("timeNamespace"), str)
            or not identity["timeNamespace"].strip()):
        raise ValueError(f"{field} timing identity is invalid")
    previous = 0
    for endpoint in ("start", "end"):
        for source in ("cpu", "runqlat"):
            span = value.get(endpoint, {}).get(source, {})
            try:
                raw_before, raw_after = span["beforeNs"], span["afterNs"]
                if (isinstance(raw_before, bool) or isinstance(raw_after, bool)
                        or not isinstance(raw_before, (int, str)) or not isinstance(raw_after, (int, str))
                        or (isinstance(raw_before, str) and not raw_before.isdigit())
                        or (isinstance(raw_after, str) and not raw_after.isdigit())):
                    raise ValueError
                before, after = int(raw_before), int(raw_after)
            except (KeyError, TypeError, ValueError) as error:
                raise ValueError(f"{field} timing span is invalid") from error
            if before <= 0 or before < previous or after < before:
                raise ValueError(f"{field} timing spans are backwards")
            previous = after
    return identity


def load_pairs(directory):
    captures = {}
    for path in directory.glob("round-*-h*-g*-*.json"):
        match = CAPTURE.fullmatch(path.name)
        if not match:
            continue
        round_number, host_load, guest_workers, domain = match.groups()
        key = (int(round_number), int(host_load), int(guest_workers))
        captures.setdefault(key, {})[domain] = json.loads(path.read_text())
    if not captures:
        raise ValueError("no M10c captures found")
    rounds = sorted({key[0] for key in captures})
    if rounds != list(range(1, rounds[-1] + 1)):
        raise ValueError("round numbers must be contiguous from 1")
    for round_number in rounds:
        for host_load, guest_workers in QUADRANTS:
            pair = captures.get((round_number, host_load, guest_workers), {})
            if set(pair) != {"host", "guest"}:
                raise ValueError(f"round {round_number} h{host_load} g{guest_workers} is incomplete")
    return captures, rounds


def load_contender_evidence(directory, setup):
    try:
        inspect = json.loads((directory / "host-contention-cri.json").read_text())
        affinity = (directory / "host-contention-affinity.txt").read_text()
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError("Host contender CRI/affinity evidence is missing or invalid") from error
    labels = inspect.get("status", {}).get("labels", {})
    evidence = {
        "pid": integer(inspect.get("info", {}).get("pid"), "Host contender CRI PID", positive=True),
        "podUid": labels.get("io.kubernetes.pod.uid"),
    }
    if not isinstance(evidence["podUid"], str) or not evidence["podUid"]:
        raise ValueError("Host contender CRI Pod UID is invalid")
    affinity_cpus = re.findall(r"new affinity list:\s*([^\r\n]+)", affinity)
    if not affinity_cpus or any(value.strip() != str(setup["pinnedHostCpu"]) for value in affinity_cpus):
        raise ValueError("Host contender affinity evidence contradicts setup.json")
    return evidence


def validate_condition(directory, label, host_load, guest_workers, setup, contender_evidence):
    try:
        condition = json.loads((directory / f"{label}-condition.json").read_text())
        workload = (directory / f"{label}-workload.txt").read_text()
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError(f"{label} condition evidence is missing or invalid") from error
    condition_workers = integer(condition.get("guestWorkers"), f"{label} Guest workers", positive=True)
    if (condition.get("hostContention") is not bool(host_load)
            or condition_workers != guest_workers):
        raise ValueError(f"{label} condition evidence contradicts its capture name")
    contender = condition.get("hostContender")
    if host_load:
        if not isinstance(contender, dict):
            raise ValueError(f"{label} Host contender evidence is invalid")
        contender_pid = integer(contender.get("pid"), f"{label} contender PID", positive=True)
        contender_cpu = integer(contender.get("pinnedHostCpu"), f"{label} contender CPU", nonnegative=True)
        if (not isinstance(contender.get("podUid"), str) or not contender["podUid"]
                or contender.get("phase") != "Running" or contender_pid <= 0
                or contender_cpu != setup["pinnedHostCpu"]):
            raise ValueError(f"{label} Host contender evidence is invalid")
        if (contender_pid != contender_evidence["pid"]
                or contender["podUid"] != contender_evidence["podUid"]):
            raise ValueError(f"{label} Host contender does not match CRI evidence")
    elif contender is not None:
        raise ValueError(f"{label} unexpectedly records a Host contender")
    ready = re.compile(rf"^running {guest_workers} worker\(s\) on CPU 0 for ", re.MULTILINE)
    if not ready.search(workload):
        raise ValueError(f"{label} Guest worker evidence is invalid")


def extract(pair, label, window_seconds, setup, guest_workers):
    host, guest = pair["host"], pair["guest"]
    if host.get("domain") != "host" or guest.get("domain") != "guest":
        raise ValueError(f"{label} domain mismatch")
    if guest.get("sandboxId") != setup["sandboxId"]:
        raise ValueError(f"{label} Sandbox differs from setup.json")
    for domain, response in (("host", host), ("guest", guest)):
        duration = number(response.get("durationSeconds"), f"{label} {domain} duration", positive=True)
        if duration < window_seconds:
            raise ValueError(f"{label} {domain} duration is shorter than the requested window")
    validate_timing(host.get("timing"), f"{label} host")
    guest_identity = validate_timing(guest.get("timing"), f"{label} guest")
    if guest.get("bootId") != guest_identity["bootId"]:
        raise ValueError(f"{label} Guest timing boot identity mismatch")
    processes = guest.get("processes")
    if (not isinstance(processes, list) or len(processes) != 1
            or not isinstance(processes[0], dict) or processes[0].get("command") != "guest-burn"):
        raise ValueError(f"{label} must contain exactly one guest-burn process")
    process = processes[0]
    items = host.get("items")
    if not isinstance(items, list) or not all(isinstance(item, dict) for item in items):
        raise ValueError(f"{label} Host items are invalid")
    matching = [item for item in items
                if item.get("kind") == "kata-sandbox" and item.get("sandboxId") == guest.get("sandboxId")]
    if len(matching) != 1 or matching[0].get("podUid") != guest.get("podUid"):
        raise ValueError(f"{label} Host/Guest target identity mismatch")
    item = matching[0]
    if integer(item.get("pid"), f"{label} VMM PID", positive=True) != setup["vmmPid"]:
        raise ValueError(f"{label} Host VMM differs from setup.json")
    threads = item.get("threads")
    if not isinstance(threads, list) or not all(isinstance(thread, dict) for thread in threads):
        raise ValueError(f"{label} Host threads are invalid")
    vcpu0 = [thread for thread in threads if thread.get("role") == "vcpu" and thread.get("vcpuIndex") == 0]
    if (len(vcpu0) != 1
            or integer(vcpu0[0].get("tid"), f"{label} vCPU 0 TID", positive=True) != setup["vcpuHostTid"]):
        raise ValueError(f"{label} must contain exactly one Host vCPU 0 thread")
    guest_cpu, guest_lat = process.get("cpu"), process.get("runqlat")
    host_cpu, host_lat = item.get("cpu"), vcpu0[0].get("runqlat")
    if not all(isinstance(value, dict) for value in (guest_cpu, guest_lat, host_cpu, host_lat)):
        raise ValueError(f"{label} required CPU/runqlat metric is null")
    coverage = process.get("runqlatCoverage")
    if not isinstance(coverage, dict):
        raise ValueError(f"{label} Guest runqlat coverage is invalid")
    observed, total = coverage.get("observedThreads"), coverage.get("totalThreads")
    if (isinstance(observed, bool) or isinstance(total, bool)
            or not isinstance(observed, int) or not isinstance(total, int)
            or observed <= 0 or observed > total):
        raise ValueError(f"{label} Guest runqlat coverage is invalid")
    if total != guest_workers + 1:
        raise ValueError(f"{label} Guest thread total does not match workload workers plus main")
    guest_count = integer(guest_lat.get("count"), f"{label} guest count", nonnegative=True)
    host_count = integer(host_lat.get("count"), f"{label} host count", nonnegative=True)
    if not isinstance(process.get("partial"), bool) or not isinstance(item.get("partial"), bool):
        raise ValueError(f"{label} partial markers must be booleans")
    return {
        "guestCpuPercent": number(guest_cpu.get("percent"), f"{label} guest CPU", positive=True),
        "guestRunqlatP95Us": number(guest_lat.get("p95Us"), f"{label} guest p95", nonnegative=True),
        "guestRunqlatCount": guest_count,
        "guestObservedThreads": observed,
        "guestTotalThreads": total,
        "guestPartial": process["partial"],
        "hostVmmCpuPercent": number(host_cpu.get("percent"), f"{label} Host CPU", positive=True),
        "hostVcpu0RunqlatP95Us": number(host_lat.get("p95Us"), f"{label} Host vCPU 0 p95", nonnegative=True),
        "hostVcpu0RunqlatCount": host_count,
        "hostPartial": item["partial"],
    }


def median(values):
    return statistics.median(values)


def summarize(directory):
    try:
        config = json.loads((directory / "config.json").read_text())
        setup = json.loads((directory / "setup.json").read_text())
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError("config.json or setup.json is missing or invalid") from error
    requested_rounds = integer(config.get("rounds"), "configured rounds", positive=True)
    window_seconds = number(config.get("windowSeconds"), "configured window", positive=True)
    if not isinstance(setup.get("sandboxId"), str) or not setup["sandboxId"]:
        raise ValueError("configured Sandbox identity is invalid")
    setup["vmmPid"] = integer(setup.get("vmmPid"), "configured VMM PID", positive=True)
    if setup.get("vcpuIndex") != 0:
        raise ValueError("configured vCPU index must be 0")
    setup["vcpuHostTid"] = integer(setup.get("vcpuHostTid"), "configured vCPU Host TID", positive=True)
    setup["pinnedHostCpu"] = integer(setup.get("pinnedHostCpu"), "configured Host CPU", nonnegative=True)
    contender_evidence = load_contender_evidence(directory, setup)
    captures, rounds = load_pairs(directory)
    if len(rounds) != requested_rounds:
        raise ValueError("capture rounds do not match config.json")
    rows = {}
    warning_windows = []
    zero_count_windows = []
    for round_number in rounds:
        for host_load, guest_workers in QUADRANTS:
            label = f"round-{round_number:02d}-h{host_load}-g{guest_workers}"
            validate_condition(directory, label, host_load, guest_workers, setup, contender_evidence)
            row = extract(captures[(round_number, host_load, guest_workers)], label, window_seconds, setup,
                          guest_workers)
            rows[(host_load, guest_workers, round_number)] = row
            if row["guestPartial"] or row["hostPartial"] or row["guestObservedThreads"] < row["guestTotalThreads"]:
                warning_windows.append(label)
            if row["guestRunqlatCount"] == 0 or row["hostVcpu0RunqlatCount"] == 0:
                zero_count_windows.append(label)
    quadrants = {}
    for host_load, guest_workers in QUADRANTS:
        group = [rows[(host_load, guest_workers, round_number)] for round_number in rounds]
        quadrants[f"h{host_load}g{guest_workers}"] = {
            "hostContention": bool(host_load),
            "guestWorkers": guest_workers,
            "samples": len(group),
            "guestCpuPercent": [row["guestCpuPercent"] for row in group],
            "guestRunqlatP95Us": [row["guestRunqlatP95Us"] for row in group],
            "guestRunqlatCount": [row["guestRunqlatCount"] for row in group],
            "hostVmmCpuPercent": [row["hostVmmCpuPercent"] for row in group],
            "hostVcpu0RunqlatP95Us": [row["hostVcpu0RunqlatP95Us"] for row in group],
            "hostVcpu0RunqlatCount": [row["hostVcpu0RunqlatCount"] for row in group],
            "medianGuestCpuPercent": median([row["guestCpuPercent"] for row in group]),
            "medianGuestRunqlatP95Us": median([row["guestRunqlatP95Us"] for row in group]),
            "medianHostVmmCpuPercent": median([row["hostVmmCpuPercent"] for row in group]),
            "medianHostVcpu0RunqlatP95Us": median([row["hostVcpu0RunqlatP95Us"] for row in group]),
            "guestCoverage": [{"observed": row["guestObservedThreads"], "total": row["guestTotalThreads"]} for row in group],
        }
    q = quadrants
    return {
        "result": "captures_valid",
        "scope": "descriptive-host-x-guest-four-quadrant-summary",
        "rounds": len(rounds),
        "windowSeconds": window_seconds,
        "capturePairs": len(rows),
        "quadrants": quadrants,
        "observedDirections": {
            "guestLoadRaisedGuestP95InBothHostStates":
                q["h0g8"]["medianGuestRunqlatP95Us"] > q["h0g1"]["medianGuestRunqlatP95Us"] and
                q["h1g8"]["medianGuestRunqlatP95Us"] > q["h1g1"]["medianGuestRunqlatP95Us"],
            "hostContentionRaisedHostVcpu0P95InBothGuestStates":
                q["h1g1"]["medianHostVcpu0RunqlatP95Us"] > q["h0g1"]["medianHostVcpu0RunqlatP95Us"] and
                q["h1g8"]["medianHostVcpu0RunqlatP95Us"] > q["h0g8"]["medianHostVcpu0RunqlatP95Us"],
        },
        "qualityWarnings": {
            "partialOrIncompleteCoverageWindows": warning_windows,
            "zeroWaitSampleWindows": zero_count_windows,
            "partialOrIncompleteCoverageCount": len(warning_windows),
            "zeroWaitSampleCount": len(zero_count_windows),
        },
        "interpretation": "Direction flags are descriptive, not pass/fail gates or causal proof.",
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    print(json.dumps(summarize(args.directory), indent=2, sort_keys=True))
