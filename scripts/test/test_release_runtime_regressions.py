import pathlib
import unittest
import yaml

ROOT = pathlib.Path(__file__).resolve().parents[2]

class ReleaseRuntimeRegressionTest(unittest.TestCase):
    def test_every_outbound_client_mounts_extra_trust(self):
        services = yaml.safe_load((ROOT / "deploy/compose/compose.release.yaml").read_text())["services"]
        for name in ("backend", "middleware", "llm-provider-server", "agent-server"):
            with self.subTest(service=name):
                self.assertTrue(any("OIDC_CA_BUNDLE" in volume for volume in services[name].get("volumes", [])), name)

    def test_llm_runtime_includes_codex_package_and_login_mount(self):
        dockerfile = (ROOT / "docker/llm-provider-server.Dockerfile").read_text()
        self.assertIn("codex-code-mode-host", dockerfile)
        self.assertIn("LLM_SERVER_CODEX_EXECUTABLE", dockerfile)
        self.assertIn("/opt/codex", dockerfile)
        overlay = ROOT / "deploy/compose/compose.codex.yaml"
        self.assertTrue(overlay.is_file(), "Codex deployment needs a persistent auth-file mount")
        service = yaml.safe_load(overlay.read_text())["services"]["llm-provider-server"]
        mount = service["volumes"][0]
        self.assertEqual(mount["target"], service["environment"]["LLM_SERVER_CODEX_AUTH_FILE"])
        self.assertIn("CODEX_AUTH_FILE:?", mount["source"])
        self.assertTrue(mount["read_only"])
        self.assertFalse(mount["bind"]["create_host_path"])

    def test_codex_ca_reaches_the_llm_process(self):
        service = yaml.safe_load((ROOT / "deploy/compose/compose.release.yaml").read_text())["services"]["llm-provider-server"]
        self.assertEqual(service["environment"]["CODEX_CA_CERTIFICATE"], "${CODEX_CA_CERTIFICATE:-}")
        self.assertTrue(any(":/etc/ssl/certs/oidc-ca.pem:ro" in volume for volume in service["volumes"]))

if __name__ == "__main__":
    unittest.main()
