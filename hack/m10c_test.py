import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("summarize_m10c", Path(__file__).with_name("summarize-m10c.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def timing():
    return {"clock": "CLOCK_MONOTONIC", "identity": {"bootId": "00112233-4455-6677-8899-aabbccddeeff", "timeNamespace": "unavailable"},
            "start": {"cpu": {"beforeNs": "1", "afterNs": "2"},
                      "runqlat": {"beforeNs": "3", "afterNs": "4"}},
            "end": {"cpu": {"beforeNs": "5", "afterNs": "6"},
                    "runqlat": {"beforeNs": "7", "afterNs": "8"}}}


class M10cSummaryTest(unittest.TestCase):
    def write_fixture(self, directory, rounds=2, partial=False):
        (directory / "config.json").write_text(json.dumps({"rounds": rounds, "windowSeconds": 3}))
        (directory / "setup.json").write_text(json.dumps({"sandboxId": "sid", "vmmPid": 10,
                                                          "vcpuIndex": 0, "vcpuHostTid": 42,
                                                          "pinnedHostCpu": 3}))
        (directory / "host-contention-cri.json").write_text(json.dumps(
            {"info": {"pid": 99}, "status": {"labels": {"io.kubernetes.pod.uid": "contender"}}}))
        (directory / "host-contention-affinity.txt").write_text("pid 99's new affinity list: 3\n")
        for round_number in range(1, rounds + 1):
            for host_load, guest_workers in module.QUADRANTS:
                stem = f"round-{round_number:02d}-h{host_load}-g{guest_workers}"
                contender = ({"podUid": "contender", "pid": 99, "phase": "Running", "pinnedHostCpu": 3}
                             if host_load else None)
                (directory / f"{stem}-condition.json").write_text(json.dumps(
                    {"hostContention": bool(host_load), "guestWorkers": guest_workers,
                     "hostContender": contender}))
                (directory / f"{stem}-workload.txt").write_text(
                    f"running {guest_workers} worker(s) on CPU 0 for 13s\n")
                guest_p95 = 10 + guest_workers * 10 + host_load
                host_p95 = 20 + host_load * 100 + guest_workers
                guest = {"domain": "guest", "podUid": "uid", "sandboxId": "sid", "durationSeconds": 3.1,
                         "bootId": "00112233-4455-6677-8899-aabbccddeeff", "timing": timing(),
                         "processes": [{"command": "guest-burn", "cpu": {"percent": 50 + guest_workers},
                                        "runqlat": {"count": 10, "p95Us": guest_p95},
                                        "runqlatCoverage": {
                                            "observedThreads": guest_workers if partial else guest_workers + 1,
                                            "totalThreads": guest_workers + 1,
                                        },
                                        "partial": partial}]}
                host = {"domain": "host", "durationSeconds": 3.1, "timing": timing(),
                         "items": [{"kind": "kata-sandbox", "sandboxId": "sid", "podUid": "uid", "pid": 10,
                                   "cpu": {"percent": 80}, "vcpuRunqlat": {"count": 20, "p95Us": host_p95},
                                   "threads": [{"role": "vcpu", "vcpuIndex": 0, "tid": 42,
                                                "runqlat": {"count": 20, "p95Us": host_p95}}],
                                   "partial": False}]}
                (directory / f"{stem}-guest.json").write_text(json.dumps(guest))
                (directory / f"{stem}-host.json").write_text(json.dumps(host))

    def test_four_quadrants_and_descriptive_directions(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            result = module.summarize(directory)
            self.assertEqual(result["result"], "captures_valid")
            self.assertEqual(result["capturePairs"], 8)
            self.assertTrue(all(result["observedDirections"].values()))
            self.assertEqual(result["qualityWarnings"]["partialOrIncompleteCoverageCount"], 0)

    def test_partial_coverage_is_visible_but_not_a_hidden_retry(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1, partial=True)
            result = module.summarize(directory)
            self.assertEqual(result["qualityWarnings"]["partialOrIncompleteCoverageCount"], 4)

    def test_zero_wait_counts_are_valid_and_visible(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g1-host.json"
            sample = json.loads(path.read_text())
            sample["items"][0]["threads"][0]["runqlat"] = {"count": 0, "p95Us": 0}
            path.write_text(json.dumps(sample))
            result = module.summarize(directory)
            self.assertEqual(result["qualityWarnings"]["zeroWaitSampleWindows"], ["round-01-h0-g1"])

    def test_unexpected_direction_does_not_fail_capture_validation(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            for path in directory.glob("*-guest.json"):
                sample = json.loads(path.read_text())
                sample["processes"][0]["runqlat"]["p95Us"] = 100 - sample["processes"][0]["runqlat"]["p95Us"]
                path.write_text(json.dumps(sample))
            result = module.summarize(directory)
            self.assertFalse(result["observedDirections"]["guestLoadRaisedGuestP95InBothHostStates"])

    def test_identity_mismatch_fails(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g1-host.json"
            sample = json.loads(path.read_text())
            sample["items"][0]["podUid"] = "wrong"
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "identity mismatch"):
                module.summarize(directory)

    def test_backwards_timing_fails(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g1-guest.json"
            sample = json.loads(path.read_text())
            sample["timing"]["end"]["cpu"]["beforeNs"] = "1"
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "backwards"):
                module.summarize(directory)

    def test_short_capture_fails(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g1-host.json"
            sample = json.loads(path.read_text())
            sample["durationSeconds"] = 2.9
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "shorter"):
                module.summarize(directory)

    def test_condition_file_cannot_relabel_a_quadrant(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g1-condition.json"
            condition = json.loads(path.read_text())
            condition["hostContention"] = True
            path.write_text(json.dumps(condition))
            with self.assertRaisesRegex(ValueError, "contradicts"):
                module.summarize(directory)

    def test_condition_must_match_cri_evidence(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h1-g1-condition.json"
            condition = json.loads(path.read_text())
            condition["hostContender"]["pid"] = 100
            path.write_text(json.dumps(condition))
            with self.assertRaisesRegex(ValueError, "does not match CRI"):
                module.summarize(directory)

    def test_worker_count_must_match_guest_thread_total(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            path = directory / "round-01-h0-g8-guest.json"
            sample = json.loads(path.read_text())
            sample["processes"][0]["runqlatCoverage"] = {"observedThreads": 2, "totalThreads": 2}
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "does not match workload"):
                module.summarize(directory)

    def test_missing_quadrant_fails(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory, rounds=1)
            (directory / "round-01-h1-g8-host.json").unlink()
            with self.assertRaisesRegex(ValueError, "incomplete"):
                module.summarize(directory)


if __name__ == "__main__":
    unittest.main()
