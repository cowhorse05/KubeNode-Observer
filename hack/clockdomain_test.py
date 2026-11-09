import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("clockdomain", Path(__file__).with_name("validate-clockdomain.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ClockDomainTest(unittest.TestCase):
    def fixture(self, offset=0):
        base = 2**54
        return {"identity": {"bootId": "00112233-4455-6677-8899-aabbccddeeff", "timeNamespace": "time:[1]"},
                "samples": [{"beforeNs": str(base+i*1000000000+offset), "kernelNs": str(base+i*1000000000+10),
                             "afterNs": str(base+i*1000000000+20+offset), "rawBeforeNs": str(base+i*1000000000+25),
                             "monoNs": str(base+i*1000000000+30+offset), "rawAfterNs": str(base+i*1000000000+35)} for i in range(3)]}

    def test_zero_and_namespace_positive_control(self):
        self.assertTrue(module.analyze(self.fixture(), 0)["allBracketsContainZero"])
        positive = module.analyze(self.fixture(2000000000), 2000000000)
        self.assertFalse(positive["allBracketsContainZero"])
        with self.assertRaises(ValueError):
            module.analyze(self.fixture(2000000000), 0)

    def test_reject_corruption(self):
        for case in ("precision", "overflow", "order", "identity", "few"):
            value = copy.deepcopy(self.fixture())
            if case == "precision": value["samples"][0]["kernelNs"] = float(2**54)
            if case == "overflow": value["samples"][0]["kernelNs"] = str(2**63)
            if case == "order": value["samples"][1]["beforeNs"] = "1"
            if case == "identity": value["identity"]["bootId"] = "bad"
            if case == "few": value["samples"] = value["samples"][:1]
            with self.subTest(case=case), self.assertRaises(ValueError): module.analyze(value)


if __name__ == "__main__":
    unittest.main()
