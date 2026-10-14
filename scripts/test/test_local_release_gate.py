import copy
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "local-test"))
from verify_release import (
    ReleaseVerificationError,
    verify_release_configuration,
)


class LocalReleaseGateTest(unittest.TestCase):
    def setUp(self):
        self.digests = {
            name: f"127.0.0.1:5000/stock-ai/{name}@sha256:{index:064x}"
            for index, name in enumerate(("backend", "agent-server", "llm-provider-server", "console", "oidc-gateway"), 1)
        }
        self.manifest = {
            "schema": "stock-ai.promotion-manifest.v1",
            "status": "promoted",
            "environment": "Test",
            "images": {name: self.digests[name] for name in ("backend", "agent-server", "llm-provider-server", "console")},
            "test_stack_images": self.digests,
            "artifacts": {name: {"build_digest": value.rsplit("@", 1)[1]} for name, value in self.digests.items()},
        }
        self.compose = {"services": {name: {"image": self.manifest["images"][name]} for name in self.manifest["images"]}}

    def test_exact_test_digests_are_consumed(self):
        self.assertEqual(verify_release_configuration(self.manifest, self.compose), self.manifest["images"])

    def test_substitution_and_build_directive_are_rejected(self):
        substitution = copy.deepcopy(self.compose)
        substitution["services"]["backend"]["image"] = self.digests["backend"].rsplit("@", 1)[0] + "@sha256:" + "f" * 64
        with self.assertRaisesRegex(ReleaseVerificationError, "differs from Test promotion"):
            verify_release_configuration(self.manifest, substitution)
        build = copy.deepcopy(self.compose)
        build["services"]["console"]["build"] = "."
        with self.assertRaisesRegex(ReleaseVerificationError, "build directive"):
            verify_release_configuration(self.manifest, build)


if __name__ == "__main__":
    unittest.main()
