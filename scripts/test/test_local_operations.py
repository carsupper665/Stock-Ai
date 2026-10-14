import hashlib
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = Path(__file__).resolve().parents[1] / "local_operations.py"
SPEC = importlib.util.spec_from_file_location("local_operations", MODULE_PATH)
local_operations = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(local_operations)


def refs(prefix="registry.local/stock-ai"):
    return {
        "backend": f"{prefix}/backend@sha256:{'a' * 64}",
        "llm-provider-server": f"{prefix}/llm@sha256:{'b' * 64}",
        "agent-server": f"{prefix}/agent@sha256:{'c' * 64}",
        "console": f"{prefix}/console@sha256:{'d' * 64}",
    }


class LocalOperationsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="operator path with spaces ")
        self.root = Path(self.temp.name)
        self.env_file = self.root / "local test.env"
        self.compose = self.root / "compose file.yaml"
        self.compose.write_text("services: {}\n", encoding="utf-8")
        self.old = {
            "backend": "stock-ai/backend:0.7.0",
            "llm-provider-server": "stock-ai/llm-provider-server:0.7.0",
            "agent-server": "stock-ai/agent-server:0.7.0",
            "console": "stock-ai/console:0.7.0",
        }
        self.write_env(self.old)
        self.evidence = self.root / "operator evidence"

    def tearDown(self):
        self.temp.cleanup()

    def write_env(self, images):
        lines = [
            "STOCK_AI_INSTANCE=local-test-unique",
            "STOCK_AI_ENVIRONMENT=Test",
            "STOCK_AI_SECRET_NAMESPACE=local-test",
            "STOCK_AI_SECRETS_DIR=/secrets with spaces",
            "LLM_SERVER_ADMIN_TOKEN=must-not-be-reported",
        ]
        for service, variable in local_operations.IMAGE_VARIABLES.items():
            if service in images:
                lines.append(f"{variable}={images[service]}")
        self.env_file.write_text("\n".join(lines) + "\n", encoding="utf-8")

    def operator(self):
        return local_operations.Operator(self.env_file, [self.compose], self.evidence, 5)

    def config_from_env(self):
        values = local_operations.read_env(self.env_file)
        return {
            "name": "local-test-unique",
            "services": {
                service: {"image": values[variable], "networks": {"app": {}}}
                for service, variable in local_operations.IMAGE_VARIABLES.items()
                if service != "oidc-gateway"
            },
        }

    def make_manifest(self, images=None):
        directory = self.root / "promotion directory"
        directory.mkdir(exist_ok=True)
        path = directory / "promotion.json"
        images = images or refs()
        test_images = dict(images)
        test_images["oidc-gateway"] = f"registry.local/stock-ai/gateway@sha256:{'e' * 64}"
        artifacts = {}
        all_files = []

        def artifact_ref(name, content):
            artifact_path = directory / name
            artifact_path.parent.mkdir(parents=True, exist_ok=True)
            artifact_path.write_text(content, encoding="utf-8")
            digest = hashlib.sha256(artifact_path.read_bytes()).hexdigest()
            all_files.append((name, digest))
            return {"file": name, "sha256": digest}

        for service in test_images:
            artifacts[service] = {
                "build_digest": test_images[service].rsplit("@", 1)[1] if "@" in test_images[service] else "",
                "registry_reference": test_images[service].split("@", 1)[0],
                "sbom": artifact_ref(f"artifacts/{service}.sbom.json", "{}\n"),
                "scan": artifact_ref(f"artifacts/{service}.scan.json", "{}\n"),
                "filesystem_inventory": artifact_ref(f"artifacts/{service}.filesystem.json", "[]\n"),
            }
        build_provenance = artifact_ref("artifacts/build-provenance.json", "{}\n")
        build_acceptance = artifact_ref("artifacts/build-acceptance.json", "{}\n")
        certification = {
            "file": "certification.json",
            "sha256": hashlib.sha256(b"{}\n").hexdigest(),
            "results": {},
        }
        result_names = sorted(local_operations.REQUIRED_ACCEPTANCE_RESULTS)
        for index, check_name in enumerate(result_names):
            result_name = f"certification/{check_name}.json"
            result_ref = artifact_ref(result_name, f'{{"check":"{check_name}"}}\n')
            certification["results"][check_name] = result_ref
        cert_file = artifact_ref("certification.json", "{}\n")
        certification.update(cert_file)
        manifest = {
            "schema": local_operations.PROMOTION_SCHEMA,
            "status": "promoted",
            "environment": "Test",
            "git_revision": "f" * 40,
            "configuration_schema_version": 1,
            "trust": {"mechanism": "SHA-256 sidecar", "checksum_file": "SHA256SUMS"},
            "acceptance_results": {name: {"status": "passed"} for name in result_names},
            "images": images,
            "test_stack_images": test_images,
            "artifacts": artifacts,
            "build_provenance": build_provenance,
            "build_acceptance": build_acceptance,
            "certification_evidence": certification,
        }
        content = json.dumps(manifest, sort_keys=True).encode()
        path.write_bytes(content)
        all_files.append(("promotion.json", hashlib.sha256(content).hexdigest()))
        (directory / "SHA256SUMS").write_text(
            "".join(f"{digest}  {name}\n" for name, digest in all_files), encoding="utf-8"
        )
        return path

    def test_paths_with_spaces_are_passed_as_literal_arguments(self):
        operator = self.operator()
        command = operator.compose_args("config", "--format", "json")
        self.assertEqual(command[3], str(self.env_file.resolve()))
        self.assertEqual(command[5], str(self.compose.resolve()))
        self.assertEqual(command.count(str(self.env_file.resolve())), 1)
        self.assertEqual(command.count(str(self.compose.resolve())), 1)

    def test_env_file_image_assignment_overrides_process_environment(self):
        with patch.dict("os.environ", {"BACKEND_IMAGE": "attacker/backend:latest"}):
            operator = self.operator()
        self.assertNotIn("BACKEND_IMAGE", operator.compose_environment)

    def test_preflight_writes_nonsecret_report_and_checks_engine_resources(self):
        operator = self.operator()
        self.env_file.chmod(0o600)
        operator.compose_config = self.config_from_env
        engine = {"OSType": "linux", "NCPU": 4, "MemTotal": 8 * 1024**3}
        result = subprocess.CompletedProcess([], 0, json.dumps(engine), "")
        with patch.object(local_operations, "run", return_value=result):
            report, code = operator.preflight()
        self.assertEqual(code, 0)
        self.assertTrue(report["passed"])
        self.assertEqual(report["windows_restart_and_ntfs"], "not_run")
        evidence = json.loads((self.evidence / "preflight.json").read_text(encoding="utf-8"))
        self.assertNotIn("must-not-be-reported", json.dumps(evidence))

    def test_ingress_is_nginx_only_loopback_on_explicit_environment_ports(self):
        operator = self.operator()
        config = self.config_from_env()
        config["services"]["backend"]["ports"] = [{"host_ip": "127.0.0.1", "published": "7794", "target": 7794}]
        self.assertTrue(any("Nginx is the only ingress" in issue for issue in operator.validate_config(config)))

        del config["services"]["backend"]["ports"]
        config["services"]["nginx"] = {"image": "nginx:stable", "ports": [{"host_ip": "0.0.0.0", "published": "443", "target": 8443}]}
        self.assertTrue(any("loopback only" in issue for issue in operator.validate_config(config)))

        config["services"]["nginx"]["ports"] = [{"host_ip": "127.0.0.1", "published": "8080", "target": 8443}]
        self.assertTrue(any("ingress contract" in issue for issue in operator.validate_config(config)))

        config["services"]["nginx"]["ports"] = [{"host_ip": "127.0.0.1", "published": "443", "target": 8443}]
        self.assertFalse(any("ingress" in issue for issue in operator.validate_config(config)))
        operator.values["STOCK_AI_ENVIRONMENT"] = "Release"
        config["services"]["nginx"]["ports"] = [{"host_ip": "127.0.0.1", "published": "8443", "target": 8443}]
        self.assertFalse(any("ingress" in issue for issue in operator.validate_config(config)))
        config["services"]["nginx"]["ports"] = [{"host_ip": "127.0.0.1", "published": "443", "target": 8443}]
        self.assertTrue(any("ingress contract" in issue for issue in operator.validate_config(config)))

    def test_backup_freshness_ignores_logs_markers_and_unverified_artifacts(self):
        directory = self.root / "backups"
        directory.mkdir()
        (directory / "recovery.log").write_text("recent", encoding="utf-8")
        (directory / "off-host-marker.json").write_text("recent", encoding="utf-8")
        self.assertEqual(local_operations.backup_freshness(directory), (None, 0))

        artifact = directory / "local-test-20261013T010000Z.sai-backup"
        artifact.write_bytes(b"encrypted backup bytes")
        self.assertEqual(local_operations.backup_freshness(directory), (None, 0))
        digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
        Path(str(artifact) + ".sha256").write_text(f"{digest}  {artifact.name}\n", encoding="utf-8")
        age, count = local_operations.backup_freshness(directory)
        self.assertEqual(count, 1)
        self.assertIsInstance(age, int)

        Path(str(artifact) + ".sha256").write_text(f"{'0' * 64}  {artifact.name}\n", encoding="utf-8")
        self.assertEqual(local_operations.backup_freshness(directory), (None, 0))

    def test_stop_uses_graceful_edge_to_core_order_and_keeps_volumes(self):
        operator = self.operator()
        operator.compose_config = lambda: {
            "services": {name: {} for name in ("backend", "llm-provider-server", "agent-server", "console", "oidc-gateway", "nginx")}
        }
        stops = []

        def fake_compose(*args, **kwargs):
            stops.append(args[-1])
            return subprocess.CompletedProcess([], 0, "", "")

        operator.compose_run = fake_compose
        operator.container = lambda service: None
        result = operator.stop()
        self.assertEqual(stops, ["nginx", "console", "oidc-gateway", "agent-server", "llm-provider-server", "backend"])
        self.assertFalse(result["volumes_removed"])

    def test_test_tags_allowed_but_release_tags_fail_closed(self):
        operator = self.operator()
        config = self.config_from_env()
        self.assertEqual(operator.validate_config(config), [])
        operator.values["STOCK_AI_ENVIRONMENT"] = "Release"
        issues = operator.validate_config(config)
        self.assertEqual(len(issues), 4)
        self.assertTrue(all("mutable" in issue for issue in issues))
        digest_config = {"services": {name: {"image": ref} for name, ref in refs().items()}}
        self.assertEqual(operator.validate_config(digest_config), [])

    def test_unreviewed_mixed_service_and_build_directive_are_rejected(self):
        operator = self.operator()
        config = self.config_from_env()
        config["services"]["legacy-agent"] = {"image": "legacy:latest"}
        self.assertTrue(any("legacy" in issue for issue in operator.validate_config(config)))
        config["services"].pop("legacy-agent")
        config["services"]["backend"]["build"] = {"context": "."}
        self.assertTrue(any("build directive" in issue for issue in operator.validate_config(config)))

    def test_promotion_requires_exact_service_digests_and_checksum(self):
        operator = self.operator()
        config = self.config_from_env()
        manifest = self.make_manifest()
        verified, checksum = operator.promotion(manifest, config)
        self.assertEqual(verified, refs())
        self.assertEqual(checksum, hashlib.sha256(manifest.read_bytes()).hexdigest())
        document = json.loads(manifest.read_text(encoding="utf-8"))
        self.assertNotIn("build_provenance", document["artifacts"]["backend"])
        self.assertNotIn("build_acceptance", document["artifacts"]["backend"])
        self.assertIn("build_provenance", document)
        self.assertIn("build_acceptance", document)
        self.assertEqual(
            set(document["artifacts"]["backend"]),
            {"build_digest", "registry_reference", "sbom", "scan", "filesystem_inventory"},
        )
        self.assertEqual(set(document["acceptance_results"]), local_operations.REQUIRED_ACCEPTANCE_RESULTS)
        self.assertEqual(set(document["certification_evidence"]["results"]), local_operations.REQUIRED_ACCEPTANCE_RESULTS)
        self.assertEqual(
            len({result["file"] for result in document["certification_evidence"]["results"].values()}),
            len(local_operations.REQUIRED_ACCEPTANCE_RESULTS),
        )

        sbom = manifest.parent / "artifacts/backend.sbom.json"
        sbom.write_text("tampered\n", encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "artifact checksum failed"):
            operator.promotion(manifest, config)
        manifest = self.make_manifest()

        checksum_file = manifest.parent / "SHA256SUMS"
        checksum_file.write_text(f"{'0' * 64}  promotion.json\n", encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "SHA256SUMS"):
            operator.promotion(manifest, config)

        mutable = refs()
        mutable["backend"] = "registry.local/backend:latest"
        invalid = self.make_manifest(mutable)
        with self.assertRaisesRegex(local_operations.WorkflowError, "sha256"):
            operator.promotion(invalid, config)

        missing = refs()
        del missing["console"]
        invalid = self.make_manifest(missing)
        with self.assertRaisesRegex(local_operations.WorkflowError, "four application images"):
            operator.promotion(invalid, config)

    def test_promotion_requires_top_level_t10_provenance_and_acceptance_refs(self):
        operator = self.operator()
        config = self.config_from_env()
        manifest = self.make_manifest()
        document = json.loads(manifest.read_text(encoding="utf-8"))
        document.pop("build_provenance")
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "top-level build_provenance"):
            operator.promotion(manifest, config)

        manifest = self.make_manifest()
        document = json.loads(manifest.read_text(encoding="utf-8"))
        document.pop("build_acceptance")
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "top-level build_acceptance"):
            operator.promotion(manifest, config)

    def test_promotion_rejects_eight_unknown_acceptance_names(self):
        operator = self.operator()
        mutations = (
            lambda value: value.__setitem__("acceptance_results", {f"gate-{i}": {"status": "passed"} for i in range(8)}),
            lambda value: value["acceptance_results"].pop("wrong_user"),
            lambda value: value["acceptance_results"].__setitem__("extra_gate", {"status": "passed"}),
        )
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                manifest = self.make_manifest()
                document = json.loads(manifest.read_text(encoding="utf-8"))
                mutate(document)
                manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
                with self.assertRaises(local_operations.WorkflowError):
                    operator.promotion(manifest, self.config_from_env())

    def test_certification_results_must_match_named_gates_and_be_distinct(self):
        operator = self.operator()
        mutations = (
            lambda results: results.__setitem__("unrelated_check", results.pop("secured_product_flow")),
            lambda results: results.pop("wrong_user"),
            lambda results: results.__setitem__("extra_gate", results["wrong_user"]),
        )
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                manifest = self.make_manifest()
                document = json.loads(manifest.read_text(encoding="utf-8"))
                mutate(document["certification_evidence"]["results"])
                manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
                with self.assertRaises(local_operations.WorkflowError):
                    operator.promotion(manifest, self.config_from_env())

        manifest = self.make_manifest()
        document = json.loads(manifest.read_text(encoding="utf-8"))
        document["certification_evidence"]["results"]["unrelated_check"] = document["certification_evidence"]["results"].pop("secured_product_flow")
        document["acceptance_results"]["unrelated_check"] = document["acceptance_results"].pop("secured_product_flow")
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "exact required acceptance"):
            operator.promotion(manifest, self.config_from_env())

        manifest = self.make_manifest()
        document = json.loads(manifest.read_text(encoding="utf-8"))
        document["acceptance_results"]["wrong_user"] = document["acceptance_results"].pop("secured_product_flow")
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "names do not match"):
            operator.promotion(manifest, self.config_from_env())

        manifest = self.make_manifest()
        document = json.loads(manifest.read_text(encoding="utf-8"))
        result_names = sorted(document["certification_evidence"]["results"])
        document["certification_evidence"]["results"][result_names[1]] = document["certification_evidence"]["results"][result_names[0]]
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "distinct checksummed refs"):
            operator.promotion(manifest, self.config_from_env())

    def test_promotion_rejects_failed_acceptance_and_maps_test_gateway_artifact(self):
        operator = self.operator()
        config = self.config_from_env()
        manifest = self.make_manifest()
        config["services"]["oidc-gateway"] = {"image": "stock-ai/gateway:test"}
        mapped, _ = operator.promotion(manifest, config)
        self.assertEqual(mapped["oidc-gateway"], f"registry.local/stock-ai/gateway@sha256:{'e' * 64}")

        document = json.loads(manifest.read_text(encoding="utf-8"))
        document["acceptance_results"]["test_material_absence"]["status"] = "not_run"
        manifest.write_text(json.dumps(document, sort_keys=True), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "required passed acceptance"):
            operator.promotion(manifest, config)

    def test_release_preflight_requires_manifest_and_exact_certified_app_digests(self):
        manifest = self.make_manifest()
        self.write_env(refs())
        contents = self.env_file.read_text(encoding="utf-8").replace("STOCK_AI_ENVIRONMENT=Test", "STOCK_AI_ENVIRONMENT=Release")
        self.env_file.write_text(contents, encoding="utf-8")
        self.env_file.chmod(0o600)
        operator = self.operator()
        operator.compose_config = self.config_from_env
        engine = {"OSType": "linux", "NCPU": 4, "MemTotal": 8 * 1024**3}
        docker_result = subprocess.CompletedProcess([], 0, json.dumps(engine), "")
        with patch.object(local_operations, "run", return_value=docker_result):
            report, code = operator.preflight(manifest)
        self.assertEqual(code, 0, report["issues"])

        release_with_gateway = self.config_from_env()
        release_with_gateway["services"]["oidc-gateway"] = {
            "image": f"registry.local/stock-ai/gateway@sha256:{'e' * 64}"
        }
        operator.compose_config = lambda: release_with_gateway
        with patch.object(local_operations, "run", return_value=docker_result):
            gateway_report, gateway_code = operator.preflight(manifest)
        self.assertEqual(gateway_code, 0, gateway_report["issues"])
        release_with_gateway["services"]["oidc-gateway"]["image"] = f"registry.local/stock-ai/gateway@sha256:{'f' * 64}"
        with patch.object(local_operations, "run", return_value=docker_result):
            gateway_mismatch, gateway_code = operator.preflight(manifest)
        self.assertEqual(gateway_code, 1)
        self.assertTrue(any("Gateway image differs" in issue for issue in gateway_mismatch["issues"]))

        with patch.object(local_operations, "run", return_value=docker_result):
            missing, missing_code = operator.preflight()
        self.assertEqual(missing_code, 1)
        self.assertTrue(any("requires the checksummed" in issue for issue in missing["issues"]))

        changed = refs()
        changed["backend"] = "registry.local/backend@sha256:" + "f" * 64
        self.write_env(changed)
        operator = self.operator()
        operator.values["STOCK_AI_ENVIRONMENT"] = "Release"
        operator.compose_config = self.config_from_env
        with patch.object(local_operations, "run", return_value=docker_result):
            mismatch, mismatch_code = operator.preflight(manifest)
        self.assertEqual(mismatch_code, 1)
        self.assertTrue(any("differ from the checksummed promotion" in issue for issue in mismatch["issues"]))

    def test_update_and_metadata_driven_rollback_restore_exact_images(self):
        operator = self.operator()
        manifest = self.make_manifest()
        operator.compose_config = self.config_from_env
        operator.wait_healthy = lambda config: None
        successful = subprocess.CompletedProcess([], 0, "", "")
        with patch.object(local_operations, "run", return_value=successful):
            result = operator.update(manifest)
            self.assertTrue(result["updated"])
            self.assertEqual(local_operations.read_env(self.env_file)["BACKEND_IMAGE"], refs()["backend"])
            metadata = json.loads(Path(result["rollback_metadata"]).read_text(encoding="utf-8"))
            self.assertEqual(metadata["previous_images"], self.old)
            self.assertEqual(metadata["promoted_images"], refs())
            self.assertIn("compose_inputs_sha256", metadata)
            self.assertIn("normalized_compose_sha256", metadata)
            self.assertIn("non_image_env_sha256", metadata)
            self.assertNotIn("must-not-be-reported", json.dumps(metadata))

            rollback = operator.rollback(Path(result["rollback_metadata"]))
        self.assertTrue(rollback["rolled_back"])
        self.assertEqual(
            {service: local_operations.read_env(self.env_file)[variable]
             for service, variable in local_operations.IMAGE_VARIABLES.items() if service in self.old},
            self.old,
        )

    def _update_for_drift_test(self, operator):
        manifest = self.make_manifest()
        operator.compose_config = self.config_from_env
        operator.wait_healthy = lambda config: None
        successful = subprocess.CompletedProcess([], 0, "", "")
        with patch.object(local_operations, "run", return_value=successful):
            result = operator.update(manifest)
        return Path(result["rollback_metadata"])

    def test_rollback_rejects_compose_input_and_normalized_config_drift(self):
        operator = self.operator()
        metadata_path = self._update_for_drift_test(operator)
        self.compose.write_text("services: {} # compose input drift\n", encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "Compose inputs/config"):
            operator.rollback(metadata_path)

        self.compose.write_text("services: {}\n", encoding="utf-8")
        config = self.config_from_env()
        config["services"]["backend"]["labels"] = {"unexpected": "topology-drift"}
        operator.compose_config = lambda: config
        with self.assertRaisesRegex(local_operations.WorkflowError, "Compose inputs/config"):
            operator.rollback(metadata_path)

    def test_rollback_rejects_non_image_environment_drift(self):
        operator = self.operator()
        metadata_path = self._update_for_drift_test(operator)
        with self.env_file.open("a", encoding="utf-8") as stream:
            stream.write("OIDC_AUDIENCE=changed-after-update\n")
        operator.compose_config = self.config_from_env
        with self.assertRaisesRegex(local_operations.WorkflowError, "non-image env values"):
            operator.rollback(metadata_path)

    def test_release_rollback_rejects_mutable_previous_images(self):
        operator = self.operator()
        promoted = refs()
        self.write_env(promoted)
        operator.values["STOCK_AI_ENVIRONMENT"] = "Release"
        operator.compose_config = self.config_from_env
        metadata_path = self.evidence / "rollback.json"
        metadata_path.parent.mkdir(parents=True)
        metadata_path.write_text(json.dumps({
            "schema_version": local_operations.SCHEMA_VERSION,
            "project": "local-test-unique",
            "previous_images": self.old,
            "promoted_images": promoted,
            "compose_inputs_sha256": operator.compose_inputs_fingerprint(),
            "normalized_compose_sha256": local_operations.config_fingerprint(operator.compose_config(), set(promoted)),
            "non_image_env_sha256": operator.non_image_env_fingerprint(),
        }), encoding="utf-8")
        with self.assertRaisesRegex(local_operations.WorkflowError, "mutable previous"):
            operator.rollback(metadata_path)

    def test_failed_docker_output_is_not_relayed(self):
        with patch.object(
            local_operations.subprocess,
            "run",
            return_value=subprocess.CompletedProcess([], 23, "private-token-value", "private-token-value"),
        ):
            with self.assertRaises(local_operations.WorkflowError) as caught:
                local_operations.run(["docker", "compose", "config"])
        self.assertNotIn("private-token-value", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
