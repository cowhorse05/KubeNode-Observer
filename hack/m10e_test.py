import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location("summarize_m10e", Path(__file__).with_name("summarize-m10e.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class M10eSummaryTest(unittest.TestCase):
    def write_fixture(self, directory, pairs=2):
        config = {
            "pairs": pairs, "durationSeconds": 10, "warmupSeconds": 3,
            "workers": 8, "workIterations": 64, "namespace": "kno-m10e-test",
            "podName": "kno-runtime-benchmark", "image": "benchmark:test",
            "kataRuntimeClass": "kno-kata-qemu",
            "resources": {"requests": {"cpu": "1", "memory": "32Mi"},
                          "limits": {"cpu": "1", "memory": "128Mi"}},
        }
        (directory / "config.json").write_text(json.dumps(config))
        for pair in range(1, pairs + 1):
            order = module.expected_order(pair)
            for position, runtime in enumerate(order, start=1):
                stem = f"pair-{pair:02d}-{position}-{runtime}"
                runtime_class = None if runtime == "runc" else "kno-kata-qemu"
                uid = f"uid-{pair}-{position}"
                (directory / f"{stem}-condition.json").write_text(json.dumps({
                    "pair": pair, "position": position, "runtime": runtime,
                    "runtimeClassName": runtime_class,
                }))
                pod = {
                    "metadata": {"uid": uid, "name": "kno-runtime-benchmark", "namespace": "kno-m10e-test"},
                    "spec": {"nodeName": "node-a", "runtimeClassName": runtime_class,
                             "containers": [{"image": "benchmark:test", "resources": config["resources"]}]},
                    "status": {"phase": "Running", "containerStatuses": [
                        {"ready": True, "restartCount": 0, "imageID": "sha256:same"}]},
                }
                (directory / f"{stem}-pod.json").write_text(json.dumps(pod))
                container = {"podUid": uid, "processVisibility": "guest-unavailable" if runtime == "kata" else "host"}
                sandboxes = [] if runtime == "runc" else [{"podUid": uid, "hypervisor": {"pid": 42}}]
                (directory / f"{stem}-topology.json").write_text(json.dumps(
                    {"snapshot": {"containers": [container], "sandboxes": sandboxes}}))
                (directory / f"{stem}-topology-deleted.json").write_text(json.dumps(
                    {"snapshot": {"containers": [], "sandboxes": []}}))
                kind = "runc-container" if runtime == "runc" else "kata-sandbox"
                (directory / f"{stem}-host.json").write_text(json.dumps(
                    {"items": [{"podUid": uid, "kind": kind, "cpu": {"percent": 1}}]}))
                operations = 10100 + pair * 100 if runtime == "runc" else 9100 + pair * 100
                duration = 10.01
                throughput = operations / duration
                p99 = 100 if runtime == "runc" else 120
                (directory / f"{stem}-workload.json").write_text(json.dumps({
                    "mode": "steady", "workers": 8, "workIterations": 64,
                    "durationSeconds": duration, "warmupSeconds": 3, "operations": operations,
                    "throughputOpsPerSecond": throughput, "p50Us": 10, "p95Us": 50,
                    "p99Us": p99, "latencyOverflow": 0,
                }))
        return config

    def test_valid_ab_ba_pairs_are_summarized(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            result = module.summarize(directory)
            self.assertEqual(result["result"], "captures_valid")
            self.assertEqual([item["order"] for item in result["pairedRounds"]], ["runc/kata", "kata/runc"])
            self.assertLess(result["effects"]["kataVsRuncThroughput"]["medianPercent"], 0)
            self.assertGreater(result["effects"]["kataVsRuncP99"]["medianPercent"], 0)

    def test_condition_cannot_relabel_runtime(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-1-runc-condition.json"
            condition = json.loads(path.read_text())
            condition["runtime"] = "kata"
            path.write_text(json.dumps(condition))
            with self.assertRaisesRegex(ValueError, "condition evidence"):
                module.summarize(directory)

    def test_runtime_class_is_verified_from_pod(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-2-kata-pod.json"
            pod = json.loads(path.read_text())
            pod["spec"]["runtimeClassName"] = None
            path.write_text(json.dumps(pod))
            with self.assertRaisesRegex(ValueError, "runtimeClassName"):
                module.summarize(directory)

    def test_omitted_empty_sandbox_collection_is_accepted_for_runc(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-1-runc-topology.json"
            topology = json.loads(path.read_text())
            del topology["snapshot"]["sandboxes"]
            path.write_text(json.dumps(topology))
            self.assertEqual(module.summarize(directory)["result"], "captures_valid")

    def test_actual_image_must_match_across_runtimes(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-2-kata-pod.json"
            pod = json.loads(path.read_text())
            pod["status"]["containerStatuses"][0]["imageID"] = "sha256:different"
            path.write_text(json.dumps(pod))
            with self.assertRaisesRegex(ValueError, "same actual imageID"):
                module.summarize(directory)

    def test_overflow_is_visible_not_a_hidden_retry(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-02-1-kata-workload.json"
            sample = json.loads(path.read_text())
            sample["latencyOverflow"] = 3
            path.write_text(json.dumps(sample))
            result = module.summarize(directory)
            self.assertEqual(result["qualityWarnings"]["latencyOverflowWindows"], ["pair-02-1-kata"])
            self.assertEqual(result["qualityWarnings"]["p99CensoredCount"], 0)

    def test_overflow_at_or_above_one_percent_marks_p99_censored(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-1-runc-workload.json"
            sample = json.loads(path.read_text())
            sample["latencyOverflow"] = sample["operations"] // 100 + 1
            sample["p99Us"] = 10000
            path.write_text(json.dumps(sample))
            result = module.summarize(directory)
            self.assertEqual(result["qualityWarnings"]["p99CensoredWindows"], ["pair-01-1-runc"])

    def test_overflow_cannot_exceed_operations(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-1-runc-workload.json"
            sample = json.loads(path.read_text())
            sample["latencyOverflow"] = sample["operations"] + 1
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "between zero and operations"):
                module.summarize(directory)

    def test_deleted_uid_cannot_remain_in_topology(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-2-kata-topology-deleted.json"
            path.write_text(json.dumps({"snapshot": {
                "containers": [{"podUid": "uid-1-2"}],
                "sandboxes": [{"podUid": "uid-1-2"}],
            }}))
            with self.assertRaisesRegex(ValueError, "remains in Agent topology"):
                module.summarize(directory)

    def test_short_or_empty_capture_fails(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            path = directory / "pair-01-1-runc-workload.json"
            sample = json.loads(path.read_text())
            sample["durationSeconds"] = 9
            path.write_text(json.dumps(sample))
            with self.assertRaisesRegex(ValueError, "shorter"):
                module.summarize(directory)

    def test_effect_direction_is_not_a_validity_gate(self):
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_fixture(directory)
            for path in directory.glob("*-kata-workload.json"):
                sample = json.loads(path.read_text())
                sample["operations"] *= 2
                sample["throughputOpsPerSecond"] = sample["operations"] / sample["durationSeconds"]
                sample["p99Us"] = 80
                sample["p95Us"] = 50
                path.write_text(json.dumps(sample))
            result = module.summarize(directory)
            self.assertGreater(result["effects"]["kataVsRuncThroughput"]["medianPercent"], 0)
            self.assertLess(result["effects"]["kataVsRuncP99"]["medianPercent"], 0)


if __name__ == "__main__":
    unittest.main()
