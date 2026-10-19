import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import validate_acceptance as gate


def records():
    checksum = "a" * 64
    names = ("backend", "agent-server", "llm-provider-server", "middleware", "nginx", "idp")
    images = {name: "fixture/" + name + "@sha256:" + checksum for name in names}
    products = {name: image for name, image in images.items() if name != "idp"}
    facts = {"history_file_count": 1, "history_sha256": checksum, "databases": {}}
    for service, tables in (("backend", ["accounts"]), ("llm-provider-server", ["provider_tokens"]), ("agent-server", ["sessions", "runs"])):
        facts["databases"][service] = {"integrity_check": "ok", "foreign_key_check": "ok", "schema_sha256": checksum,
                                      "sqlite_sequences_sha256": checksum, "table_counts": {table: 1 for table in tables},
                                      "tables": {table: {"count": 1, "all_records_sha256": checksum, "identifiers_sha256": checksum,
                                                         "representative_records_sha256": [checksum]} for table in tables}}
    routes = ("/api/v3/ticker/price", "/api/v3/exchangeInfo", "/api/v3/klines")
    business = {key: True for key in gate.BUSINESS}
    business.update(status="passed", market_boundary={"application_network_internal": True, "binance_dns_alias": "local-idp-fixture",
                    "backend_certificate_verification": "existing SSL_CERT_FILE; no bypass", "all_running_digests_match": True},
                    market_provenance={"price": 100, "symbol": "BTCUSDT", "external_calls": 0, "ticker_info_ohlcv_public_api": True,
                        "before": {"fixture": "stock-ai-local-binance-v1", "external_calls": 0, "routes": {route: 0 for route in routes}},
                        "after": {"fixture": "stock-ai-local-binance-v1", "external_calls": 0, "routes": {route: 1 for route in routes}}})
    rehearsal = {"status": "passed", "all_records_and_history_match": True, "matching_keys_restored": True, "all_services_healthy": True}
    rollback = {"project": "stock-ai-local-release", "promotion_manifest_sha256": checksum, "promoted_images": products,
                **{key: checksum for key in ("compose_inputs_sha256", "normalized_compose_sha256", "non_image_env_sha256")}}
    acceptance = {"schema": "stock-ai.t15-acceptance.v1", "status": "passed", "checkpoint": "complete", "product_images_rebuilt": False,
                  "promotion_manifest_sha256": checksum, "product_images": products, "auxiliary_image": images["idp"], "business": business,
                  "fingerprints": rollback, "failed_checkpoint_rollback": rehearsal, "predeployment_backup_sha256": checksum,
                  "accepted_snapshot_sha256": checksum, "full_profile_failed_checkpoint": {"status": "passed",
                    "all_data_history_keys_tls_fixture_env_compose_restored": True, "all_original_services_healthy": True, "named_volumes_preserved": True}}
    migration = {"schema": "stock-ai.fixture-migration.v1", "status": "passed", "business_acceptance": "passed", "source": facts,
                 "target": copy.deepcopy(facts), "predeployment": copy.deepcopy(facts), "business": copy.deepcopy(business),
                 "matching_master_and_account_keys_transferred": True, "source_credentials_authenticated": 1,
                 "failed_checkpoint_rollback": copy.deepcopy(rehearsal)}
    # A legitimate predeployment Release may be empty.
    migration["predeployment"]["history_file_count"] = 0
    runtime = {"schema": "stock-ai.local-release-runtime.v1", "status": "passed", "all_release_bind_sources_persistent": True,
               "promotion_manifest_sha256": checksum,
               "services": {name: {"image": image, "running": True, "health": "healthy"} for name, image in images.items()},
               "isolation": {kind: {"disjoint_from_test": True} for kind in ("networks", "volumes", "binds")},
               "published_ingress": [{"service": "nginx", "host_ip": "127.0.0.1", "host_port": "8443", "target": "8443/tcp"}]}
    ingress = {"schema": "stock-ai.local-release-ingress.v1", "status": "passed", "authenticated_api_status": 200,
               "anonymous_api_status": 401, "all_redirects_release_port": True, "test_origin_visited": False}
    backup = {"schema": "stock-ai.complete-release-backup.v1", "encrypted": "AES-256-GCM", "sha256": checksum,
              "authenticated_and_inventory_verified": True, "all_nine_secrets_and_tls_jwks": True}
    return {"acceptance.json": acceptance, "migration.json": migration, "runtime.json": runtime, "ingress.json": ingress,
            "rollback.json": rollback, "predeployment-backup.json": backup, "accepted-backup.json": copy.deepcopy(backup)}, images, checksum


class EvidenceGateTest(unittest.TestCase):
    def test_complete_consistent_evidence_passes_even_with_empty_prior_history(self):
        evidence, images, checksum = records()
        self.assertEqual(gate.validate_records(evidence, images, checksum)["status"], "passed")

    def test_equal_counts_do_not_hide_id_history_key_or_provenance_failures(self):
        mutations = {
            "id_change": lambda r: r["migration.json"]["target"]["databases"]["backend"]["tables"]["accounts"].update(identifiers_sha256="b" * 64),
            "history_change": lambda r: r["migration.json"]["target"].update(history_sha256="b" * 64),
            "key_not_authenticated": lambda r: r["migration.json"].update(source_credentials_authenticated=0),
            "no_actual_ticker": lambda r: r["acceptance.json"]["business"]["market_provenance"]["after"]["routes"].update({"/api/v3/ticker/price": 0}),
            "external_routing": lambda r: r["acceptance.json"]["business"]["market_boundary"].update(application_network_internal=False),
            "missing_stops": lambda r: r["acceptance.json"]["business"].update(simulated_order_persisted_with_stops=False),
            "failed_rollback": lambda r: r["acceptance.json"]["full_profile_failed_checkpoint"].update(status="failed"),
            "bad_fingerprint": lambda r: r["rollback.json"].update(non_image_env_sha256="invalid"),
            "snapshot_unverified": lambda r: r["accepted-backup.json"].update(authenticated_and_inventory_verified=False),
            "private_material": lambda r: r["migration.json"].update(token="must-never-export"),
            "digest_substitution": lambda r: r["runtime.json"]["services"]["backend"].update(image="mutable:latest"),
            "rebuild": lambda r: r["acceptance.json"].update(product_images_rebuilt=True),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                evidence, images, checksum = records()
                mutate(evidence)
                with self.assertRaises(ValueError):
                    gate.validate_records(evidence, images, checksum)

    def test_checksummed_inventory_rejects_tamper_missing_and_private_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name in gate.FILES:
                (directory / name).write_text("{}")
            checksum = hashlib.sha256(b"{}").hexdigest()
            manifest = "".join(checksum + "  " + name + "\n" for name in gate.FILES)
            gate.hashes(manifest, directory)
            (directory / gate.FILES[0]).write_text("changed")
            with self.assertRaises(ValueError):
                gate.hashes(manifest, directory)
            (directory / gate.FILES[0]).write_text("{}")
            for invalid in (manifest.splitlines()[0], manifest + checksum + "  recovery.key\n", manifest + checksum + "  ../outside\n"):
                with self.assertRaises(ValueError):
                    gate.hashes(invalid, directory)


if __name__ == "__main__":
    unittest.main()
