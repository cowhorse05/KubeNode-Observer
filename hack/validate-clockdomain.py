#!/usr/bin/env python3
"""Read-only, offline checks of kno-clockcheck captures; never a drift certificate."""
import argparse
from fractions import Fraction
import json
from pathlib import Path
import re


def ns(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]+", value):
        raise ValueError("nanoseconds must be unsigned decimal strings")
    result = int(value)
    if not 0 < result <= 2**63 - 1:
        raise ValueError("nanoseconds outside positive int64")
    return result


def analyze(report, expected_offset=None):
    identity = report["identity"]
    if not re.fullmatch(r"[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}", identity["bootId"]):
        raise ValueError("invalid boot identity")
    if not re.fullmatch(r"time:\[[0-9]+\]|unavailable", identity["timeNamespace"]):
        raise ValueError("invalid time namespace")
    samples = report["samples"]
    if not 2 <= len(samples) <= 100:
        raise ValueError("need 2..100 samples")
    intervals = []
    points = []
    previous = None
    for sample in samples:
        b, k, a, rb, m, ra = [ns(sample[key]) for key in
                              ("beforeNs", "kernelNs", "afterNs", "rawBeforeNs", "monoNs", "rawAfterNs")]
        if a < b or ra < rb or m < a or (previous and (b < previous[0] or rb < previous[1])):
            raise ValueError("clock samples backwards/out of order")
        lower, upper = b-k, a-k
        if expected_offset is not None and not lower <= expected_offset <= upper:
            raise ValueError("expected user-minus-BPF offset outside a sample bracket")
        intervals.append((lower, upper))
        points.append((rb, m, ra))
        previous = m, ra
    first, last = points[0], points[-1]
    raw_min, raw_max = last[0]-first[2], last[2]-first[0]
    if raw_min <= 0:
        raise ValueError("endpoint RAW brackets overlap")
    mono_elapsed = last[1]-first[1]
    # Mean elapsed-rate ratio over these endpoints ONLY, including read brackets.
    # No assumption of constant frequency, no wall-clock accuracy or future bound.
    rate_lower = (Fraction(mono_elapsed, raw_max)-1)*1000000
    rate_upper = (Fraction(mono_elapsed, raw_min)-1)*1000000
    return {
        "result": "passed", "samples": len(samples), "identity": identity,
        "expectedUserMinusBpfNs": None if expected_offset is None else str(expected_offset),
        "allBracketsContainZero": all(lo <= 0 <= hi for lo, hi in intervals),
        "maxKernelReadBracketNs": str(max(hi-lo for lo, hi in intervals)),
        "userMinusBpfEnvelopeNs": [str(min(lo for lo, _ in intervals)), str(max(hi for _, hi in intervals))],
        "monoElapsedNs": str(mono_elapsed), "rawElapsedBracketNs": [str(raw_min), str(raw_max)],
        "observedEndpointMonoVsRawRatePpm": [float(rate_lower), float(rate_upper)],
        "scope": "observed-endpoints-only; not a Host/Guest mapping or future drift bound"
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("capture", type=Path)
    parser.add_argument("--expect-offset-ns", type=int, default=None)
    args = parser.parse_args()
    print(json.dumps(analyze(json.loads(args.capture.read_text()), args.expect_offset_ns), indent=2))
