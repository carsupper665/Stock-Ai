import re
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = ROOT / "deploy" / "compose" / "compose.yaml"
ACCEPTANCE_PATH = ROOT / "scripts" / "test-compose-stack.ps1"
SERVICES = {"backend", "agent-server", "llm-provider-server", "console"}
PRIVATE_SERVICES = {"backend", "agent-server", "llm-provider-server"}


class ComposeStackPolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.raw = COMPOSE_PATH.read_text(encoding="utf-8")
        cls.compose = yaml.safe_load(cls.raw)
        cls.services = cls.compose["services"]

    def test_single_definition_has_exact_application_services(self):
        self.assertEqual(set(self.services), SERVICES)

    def test_no_application_service_publishes_a_host_port(self):
        for name, service in self.services.items():
            self.assertNotIn("ports", service, name)
            self.assertNotIn("network_mode", service, name)
        for name in PRIVATE_SERVICES:
            self.assertEqual(set(self.services[name]["networks"]), {"application"})

    def test_only_console_joins_designated_edge(self):
        edge_members = {
            name
            for name, service in self.services.items()
            if "edge" in service.get("networks", {})
        }
        self.assertEqual(edge_members, {"console"})
        self.assertEqual(
            self.compose["networks"]["edge"]["labels"]["stock-ai.boundary"],
            "designated-edge",
        )

    def test_cross_service_addresses_are_compose_dns_names(self):
        expected = {
            ("agent-server", "AGENT_SERVER_BACKEND_URL"): "http://backend:7794",
            ("agent-server", "AGENT_SERVER_GATEWAY_URL"): "http://llm-provider-server:8090",
            ("console", "BACKEND_URL"): "http://backend:7794",
            ("console", "AGENT_SERVER_URL"): "http://agent-server:8080",
            ("console", "LLM_SERVER_URL"): "http://llm-provider-server:8090",
        }
        for (service, variable), value in expected.items():
            self.assertEqual(self.services[service]["environment"][variable], value)
            self.assertNotRegex(value, r"localhost|127\.0\.0\.1|host\.docker\.internal")

    def test_state_uses_explicit_environment_named_volumes(self):
        expected = {
            "backend": ("backend_state", "/data"),
            "agent-server": ("agent_state", "/data"),
            "llm-provider-server": ("llm_state", "/data"),
        }
        for service, mount in expected.items():
            self.assertIn(":".join(mount), self.services[service]["volumes"])
        self.assertNotIn("volumes", self.services["console"])
        for name, volume in self.compose["volumes"].items():
            self.assertEqual(volume["name"], "${STOCK_AI_INSTANCE}_" + name)
            self.assertEqual(
                volume["labels"]["stock-ai.environment"],
                "${STOCK_AI_ENVIRONMENT}",
            )

    def test_all_dependencies_are_health_gated_and_encode_stop_order(self):
        expected_dependencies = {
            "backend": set(),
            "llm-provider-server": {"backend"},
            "agent-server": {"backend", "llm-provider-server"},
            "console": {"backend", "llm-provider-server", "agent-server"},
        }
        for service, expected in expected_dependencies.items():
            dependencies = self.services[service].get("depends_on", {})
            self.assertEqual(set(dependencies), expected, service)
            for dependency in dependencies.values():
                self.assertEqual(dependency["condition"], "service_healthy")
        # Compose creates in dependency order and removes in reverse dependency
        # order: console -> agent -> LLM gateway -> backend.
        self.assertIn("llm-provider-server", self.services["agent-server"]["depends_on"])
        self.assertIn("backend", self.services["llm-provider-server"]["depends_on"])

    def test_health_and_graceful_runtime_controls_are_explicit(self):
        for name, service in self.services.items():
            self.assertEqual(service["stop_signal"], "SIGTERM", name)
            self.assertEqual(service["restart"], "unless-stopped", name)
            self.assertTrue(service["read_only"], name)
            self.assertEqual(service["cap_drop"], ["ALL"], name)
            self.assertIn("healthcheck", service, name)
            self.assertNotIn("sleep", str(service.get("command", "")).lower(), name)
        for name in SERVICES:
            self.assertRegex(self.services[name]["stop_grace_period"], r"^[1-9][0-9]*s$")

    def test_production_secrets_are_files_not_environment_values(self):
        secret_variables = re.findall(r"\b[A-Z][A-Z0-9_]*(?:TOKEN|KEY)(?:_FILE)?\b", self.raw)
        direct = {name for name in secret_variables if not name.endswith("_FILE")}
        self.assertEqual(direct, {"ALLOW_STATIC_USER_TOKEN"})
        for secret in self.compose["secrets"].values():
            self.assertIn("${STOCK_AI_SECRET_NAMESPACE}", secret["file"])

    def test_windows_acceptance_covers_recovery_without_volume_deletion(self):
        script = ACCEPTANCE_PATH.read_text(encoding="utf-8")
        self.assertIn(".ProviderPath", script)
        self.assertIn("& $python.Command @pythonArguments | Out-Null", script)
        self.assertIn("ConvertTo-Json -InputObject @($historyEvidence)", script)
        self.assertNotIn("-LiteralPath $ComposeFile).Path", script)
        self.assertNotIn("-LiteralPath $EnvFile).Path", script)
        for evidence in (
            "crash-recovery.json",
            "before-compose-restart",
            "after-compose-restart",
            "after-stop-start",
            "after-down-up",
            "before-docker-restart",
            "after-docker-restart",
        ):
            self.assertIn(evidence, script)
        self.assertNotRegex(script, r"down[^\r\n]*(?:--volumes|-v\b)")
        self.assertNotIn("docker volume rm", script.lower())


if __name__ == "__main__":
    unittest.main()
