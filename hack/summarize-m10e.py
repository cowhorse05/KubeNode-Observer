#!/usr/bin/env python3
"""Validate and summarize the M10e runc/Kata paired runtime experiment."""

import json
import math
import random
import statistics
import sys
from pathlib import Path


SCOPE = "descriptive-runc-kata-paired-runtime-comparison"
BOOTSTRAP_SEED = 20261113
BOOTSTRAP_SAMPLES = 10_000


def load(path):
    with path.open(encoding="utf-8") as handle:
        return json.load(handle)


def finite_positive(value, label):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value <= 0:
        raise ValueError(f"{label} must be finite and positive")
    return float(value)


def effect(deltas):
    ordered = sorted(deltas)
    median = statistics.median(ordered)
    generator = random.Random(BOOTSTRAP_SEED)
    bootstrap = sorted(
        statistics.median(generator.choice(deltas) for _ in deltas)
        for _ in range(BOOTSTRAP_SAMPLES)
    )
    return {
        "pairs": len(deltas),
        "medianPercent": median,
        "bootstrapLowerPercent": bootstrap[249],
        "bootstrapUpperPercent": bootstrap[9749],
        "interpretation": "exploratory_small_n_interval",
    }


def expected_order(pair):
    return ["runc", "kata"] if pair % 2 else ["kata", "runc"]


def validate_workload(sample, config, stem):
    if sample.get("mode") != "steady":
        raise ValueError(f"{stem} workload mode is not steady")
    if sample.get("workers") != config["workers"] or sample.get("workIterations") != config["workIterations"]:
        raise ValueError(f"{stem} workload parameters contradict config")
    duration = finite_positive(sample.get("durationSeconds"), f"{stem} duration")
    if duration + 0.05 < config["durationSeconds"]:
        raise ValueError(f"{stem} workload window is shorter than configured")
    warmup = sample.get("warmupSeconds")
    if not isinstance(warmup, (int, float)) or isinstance(warmup, bool) or not math.isclose(warmup, config["warmupSeconds"]):
        raise ValueError(f"{stem} warmup contradicts config")
    operations = sample.get("operations")
    if isinstance(operations, bool) or not isinstance(operations, int) or operations <= 0:
        raise ValueError(f"{stem} operations must be positive")
    throughput = finite_positive(sample.get("throughputOpsPerSecond"), f"{stem} throughput")
    computed_throughput = operations / duration
    if not math.isclose(throughput, computed_throughput, rel_tol=1e-9, abs_tol=1e-9):
        raise ValueError(f"{stem} throughput contradicts operations and duration")
    p50 = finite_positive(sample.get("p50Us"), f"{stem} p50")
    p95 = finite_positive(sample.get("p95Us"), f"{stem} p95")
    p99 = finite_positive(sample.get("p99Us"), f"{stem} p99")
    if not (p50 <= p95 <= p99 <= 10_000):
        raise ValueError(f"{stem} latency percentiles are inconsistent")
    overflow = sample.get("latencyOverflow")
    if isinstance(overflow, bool) or not isinstance(overflow, int) or not 0 <= overflow <= operations:
        raise ValueError(f"{stem} latencyOverflow must be between zero and operations")
    return {
        "operations": operations,
        "throughputOpsPerSecond": throughput,
        "p50Us": p50,
        "p95Us": p95,
        "p99Us": p99,
        "latencyOverflow": overflow,
        "latencyOverflowRate": overflow / operations,
        "durationSeconds": duration,
    }


def validate_pod(pod, runtime, config, seen_uids, image_ids, stem):
    metadata = pod.get("metadata") or {}
    spec = pod.get("spec") or {}
    status = pod.get("status") or {}
    uid = metadata.get("uid")
    if not isinstance(uid, str) or not uid or uid in seen_uids:
        raise ValueError(f"{stem} Pod UID is missing or reused")
    seen_uids.add(uid)
    if metadata.get("namespace") != config["namespace"] or metadata.get("name") != config["podName"]:
        raise ValueError(f"{stem} Pod identity contradicts config")
    expected_class = None if runtime == "runc" else config["kataRuntimeClass"]
    if spec.get("runtimeClassName") != expected_class:
        raise ValueError(f"{stem} runtimeClassName does not identify {runtime}")
    containers = spec.get("containers") or []
    statuses = status.get("containerStatuses") or []
    if len(containers) != 1 or len(statuses) != 1 or containers[0].get("image") != config["image"]:
        raise ValueError(f"{stem} does not use the configured single workload image")
    resources = containers[0].get("resources") or {}
    if resources != config["resources"]:
        raise ValueError(f"{stem} resources contradict config")
    current = statuses[0]
    image_id = current.get("imageID")
    if not isinstance(image_id, str) or not image_id:
        raise ValueError(f"{stem} actual imageID is missing")
    image_ids.add(image_id)
    if current.get("restartCount") != 0 or not current.get("ready") or status.get("phase") != "Running":
        raise ValueError(f"{stem} Pod was not stable and Ready")
    return {"podUid": uid, "nodeName": spec.get("nodeName"), "imageID": image_id}


def validate_condition(condition, pair, position, runtime, config, stem):
    expected = {
        "pair": pair,
        "position": position,
        "runtime": runtime,
        "runtimeClassName": None if runtime == "runc" else config["kataRuntimeClass"],
    }
    if condition != expected:
        raise ValueError(f"{stem} condition evidence contradicts its paired position")


def validate_topology(topology, runtime, pod_uid, stem):
    snapshot = topology.get("snapshot") or {}
    containers = [item for item in (snapshot.get("containers") or []) if item.get("podUid") == pod_uid]
    sandboxes = [item for item in (snapshot.get("sandboxes") or []) if item.get("podUid") == pod_uid]
    if len(containers) != 1:
        raise ValueError(f"{stem} topology does not contain exactly one workload container")
    if runtime == "runc":
        if sandboxes:
            raise ValueError(f"{stem} runc capture unexpectedly has a Kata sandbox")
        if containers[0].get("processVisibility") == "guest-unavailable":
            raise ValueError(f"{stem} runc container was labelled guest-unavailable")
    else:
        if len(sandboxes) != 1 or not ((sandboxes[0].get("hypervisor") or {}).get("pid", 0) > 0):
            raise ValueError(f"{stem} Kata sandbox identity or hypervisor PID is missing")
        if containers[0].get("processVisibility") != "guest-unavailable":
            raise ValueError(f"{stem} Kata container visibility boundary is missing")


def validate_deleted_topology(topology, pod_uid, stem):
    snapshot = topology.get("snapshot") or {}
    containers = [item for item in (snapshot.get("containers") or []) if item.get("podUid") == pod_uid]
    sandboxes = [item for item in (snapshot.get("sandboxes") or []) if item.get("podUid") == pod_uid]
    if containers or sandboxes:
        raise ValueError(f"{stem} deleted Pod remains in Agent topology")


def validate_host(host, runtime, pod_uid, stem):
    items = [item for item in (host.get("items") or []) if item.get("podUid") == pod_uid]
    expected_kind = "runc-container" if runtime == "runc" else "kata-sandbox"
    if len(items) != 1 or items[0].get("kind") != expected_kind or items[0].get("cpu") is None:
        raise ValueError(f"{stem} Host resource attribution is incomplete")


def summarize(directory):
    config = load(directory / "config.json")
    required = {
        "pairs": int,
        "durationSeconds": int,
        "warmupSeconds": int,
        "workers": int,
        "workIterations": int,
        "namespace": str,
        "podName": str,
        "image": str,
        "kataRuntimeClass": str,
        "resources": dict,
    }
    for key, kind in required.items():
        if not isinstance(config.get(key), kind) or isinstance(config.get(key), bool):
            raise ValueError(f"config {key} is missing or invalid")
    if config["pairs"] < 2 or config["pairs"] % 2 or config["durationSeconds"] < 1:
        raise ValueError("config needs an even pair count >=2 and a positive duration")
    if config["namespace"] != "kno-m10e-test" or config["podName"] != "kno-runtime-benchmark":
        raise ValueError("config experiment identity is unsupported")
    if not (1 <= config["workers"] <= 64) or config["workIterations"] < 1 or config["warmupSeconds"] < 1:
        raise ValueError("config workload parameters are out of range")
    expected_resources = {
        "requests": {"cpu": "1", "memory": "32Mi"},
        "limits": {"cpu": "1", "memory": "128Mi"},
    }
    if config["resources"] != expected_resources:
        raise ValueError("config resources do not match the controlled experiment")

    seen_uids = set()
    image_ids = set()
    nodes = set()
    rounds = []
    overflow_windows = []
    p99_censored_windows = []
    throughput_deltas = []
    p99_deltas = []
    for pair in range(1, config["pairs"] + 1):
        order = expected_order(pair)
        captures = {}
        for position, runtime in enumerate(order, start=1):
            stem = f"pair-{pair:02d}-{position}-{runtime}"
            condition = load(directory / f"{stem}-condition.json")
            validate_condition(condition, pair, position, runtime, config, stem)
            pod = load(directory / f"{stem}-pod.json")
            identity = validate_pod(pod, runtime, config, seen_uids, image_ids, stem)
            nodes.add(identity["nodeName"])
            validate_topology(load(directory / f"{stem}-topology.json"), runtime, identity["podUid"], stem)
            validate_deleted_topology(
                load(directory / f"{stem}-topology-deleted.json"), identity["podUid"], stem
            )
            validate_host(load(directory / f"{stem}-host.json"), runtime, identity["podUid"], stem)
            workload = validate_workload(load(directory / f"{stem}-workload.json"), config, stem)
            if workload["latencyOverflow"]:
                overflow_windows.append(stem)
            if workload["latencyOverflowRate"] >= 0.01:
                p99_censored_windows.append(stem)
            captures[runtime] = {**workload, **identity}
        runc = captures["runc"]
        kata = captures["kata"]
        throughput_delta = (kata["throughputOpsPerSecond"] / runc["throughputOpsPerSecond"] - 1) * 100
        p99_delta = (kata["p99Us"] / runc["p99Us"] - 1) * 100
        throughput_deltas.append(throughput_delta)
        p99_deltas.append(p99_delta)
        rounds.append({
            "pair": pair,
            "order": "/".join(order),
            "runc": runc,
            "kata": kata,
            "kataVsRuncThroughputDeltaPercent": throughput_delta,
            "kataVsRuncP99DeltaPercent": p99_delta,
        })
    if len(image_ids) != 1:
        raise ValueError("runc and Kata did not execute the same actual imageID")
    if len(nodes) != 1 or None in nodes:
        raise ValueError("captures did not execute on one identified node")

    return {
        "result": "captures_valid",
        "scope": SCOPE,
        "pairs": config["pairs"],
        "captureCount": config["pairs"] * 2,
        "actualImageID": next(iter(image_ids)),
        "nodeName": next(iter(nodes)),
        "pairedRounds": rounds,
        "effects": {
            "kataVsRuncThroughput": effect(throughput_deltas),
            "kataVsRuncP99": effect(p99_deltas),
        },
        "qualityWarnings": {
            "latencyOverflowCount": len(overflow_windows),
            "latencyOverflowWindows": overflow_windows,
            "p99CensoredCount": len(p99_censored_windows),
            "p99CensoredWindows": p99_censored_windows,
        },
        "limitations": [
            "Results describe this single-node image, kernel, runtime and short benchmark only.",
            "Fresh Pods remove startup time from the workload window but cannot remove sequential host noise.",
            "Small-N bootstrap intervals are exploratory and are not a universal Kata overhead claim.",
            "Host resource captures verify attribution; they are separate from the workload measurement window.",
        ],
    }


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: summarize-m10e.py OUTPUT_DIRECTORY")
    result = summarize(Path(sys.argv[1]))
    json.dump(result, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
