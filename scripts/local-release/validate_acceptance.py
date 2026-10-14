#!/usr/bin/env python3
"""Pure offline validation of sanitized T15 evidence; no Docker/runtime mutation."""

import argparse
import hashlib
import json
import re
from pathlib import Path

from prepare import DEFAULT_MANIFEST, approved_images

FILES = ("acceptance.json", "migration.json", "runtime.json", "ingress.json", "rollback.json",
         "predeployment-backup.json", "accepted-backup.json")
SHA = re.compile(r"^[0-9a-f]{64}$")
BUSINESS = ("fixture_login", "external_and_harness_providers_disabled", "fake_provider_verified_generation",
            "fixture_market_read", "migrated_account_token_decrypted", "account_created", "agent_run_completed",
            "persisted_agent_order_verified", "simulated_order_persisted_with_stops", "session_stopped", "logout_cleared_session")
STORES = {"backend", "llm-provider-server", "agent-server"}


def require(condition, checkpoint):
    if not condition:
        raise ValueError("T15 evidence rejected at " + checkpoint)


def safe_content(value):
    if isinstance(value, dict):
        for key, item in value.items():
            require(key.lower() not in {"token", "secret", "secret_encrypted", "authorization", "cookie", "cookies",
                                        "private_key", "response_body", "password", "recovery_key"}, "secret-free-fields")
            safe_content(item)
    elif isinstance(value, list):
        for item in value:
            safe_content(item)
    elif isinstance(value, str):
        require("PRIVATE KEY-----" not in value and "Bearer " not in value, "secret-free-values")


def hashes(manifest, directory):
    entries = {}
    for line in manifest.splitlines():
        parts = line.split("  ", 1)
        require(len(parts) == 2 and SHA.fullmatch(parts[0]), "checksum-format")
        checksum, name = parts
        require(name in FILES and name not in entries, "checksum-inventory")
        path = directory / name
        require(not path.is_symlink() and path.is_file(), "regular-evidence-files")
        require(hashlib.sha256(path.read_bytes()).hexdigest() == checksum, "file-checksum")
        entries[name] = checksum
    require(set(entries) == set(FILES), "complete-evidence-inventory")


def stores(facts):
    require(set(facts) == {"databases", "history_file_count", "history_sha256"}, "store-evidence-shape")
    require(set(facts["databases"]) == STORES and type(facts["history_file_count"]) is int and
            facts["history_file_count"] >= 0 and SHA.fullmatch(facts["history_sha256"]), "all-stores-and-history")
    for database in facts["databases"].values():
        require(set(database) == {"integrity_check", "schema_sha256", "table_counts", "foreign_key_check", "tables", "sqlite_sequences_sha256"}, "database-shape")
        require(database["integrity_check"] == "ok" and database["foreign_key_check"] == "ok", "database-integrity")
        require(SHA.fullmatch(database["schema_sha256"]) and SHA.fullmatch(database["sqlite_sequences_sha256"]), "schema-sequence-digests")
        require(set(database["tables"]) == set(database["table_counts"]), "all-table-evidence")
        for name, table in database["tables"].items():
            require(set(table) == {"count", "all_records_sha256", "identifiers_sha256", "representative_records_sha256"}, "hashed-records-only")
            require(type(table["count"]) is int and table["count"] >= 0 and table["count"] == database["table_counts"][name], "table-count")
            require(SHA.fullmatch(table["all_records_sha256"]) and SHA.fullmatch(table["identifiers_sha256"]), "all-record-and-id-digests")
            representative = table["representative_records_sha256"]
            require(len(representative) == min(3, table["count"]) and all(SHA.fullmatch(value) for value in representative), "representative-records")


def validate_records(records, images, manifest_sha):
    acceptance, migration = records["acceptance.json"], records["migration.json"]
    runtime, ingress, rollback = records["runtime.json"], records["ingress.json"], records["rollback.json"]
    for record in records.values():
        safe_content(record)
    require(acceptance.get("schema") == "stock-ai.t15-acceptance.v1" and acceptance.get("status") == "passed" and acceptance.get("checkpoint") == "complete", "acceptance-complete")
    require(migration.get("schema") == "stock-ai.fixture-migration.v1" and migration.get("status") == "passed" and migration.get("business_acceptance") == "passed", "migration-business-complete")
    require(acceptance.get("product_images_rebuilt") is False, "immutable-products")
    products = {name: image for name, image in images.items() if name not in ("idp", "nginx")}
    require(acceptance.get("product_images") == products and acceptance.get("auxiliary_image") == images["idp"], "approved-digests")
    stores(migration["source"])
    stores(migration["target"])
    stores(migration["predeployment"])
    require(migration["source"] == migration["target"], "counts-ids-records-history-match")
    require(migration["source"]["history_file_count"] > 0, "representative-agent-history")
    source = migration["source"]["databases"]
    for service, table in (("backend", "accounts"), ("llm-provider-server", "provider_tokens"),
                           ("agent-server", "sessions"), ("agent-server", "runs")):
        require(source[service]["table_counts"].get(table, 0) > 0, "representative-business-data")
    require(migration.get("matching_master_and_account_keys_transferred") is True and
            migration.get("source_credentials_authenticated") == source["llm-provider-server"]["table_counts"]["provider_tokens"], "matching-key-authenticated")
    rehearsal = migration.get("failed_checkpoint_rollback", {})
    require(rehearsal.get("status") == "passed" and all(rehearsal.get(key) is True for key in
            ("all_records_and_history_match", "matching_keys_restored", "all_services_healthy")), "data-key-rollback-rehearsal")
    require(acceptance.get("failed_checkpoint_rollback") == rehearsal, "rollback-cross-record")
    full = acceptance.get("full_profile_failed_checkpoint", {})
    require(full.get("status") == "passed" and all(full.get(key) is True for key in
            ("all_data_history_keys_tls_fixture_env_compose_restored", "all_original_services_healthy", "named_volumes_preserved")), "complete-profile-rollback-rehearsal")
    business = acceptance.get("business", {})
    require(business == migration.get("business") and business.get("status") == "passed" and
            all(business.get(key) is True for key in BUSINESS), "all-business-checkpoints")
    boundary = business.get("market_boundary", {})
    require(boundary.get("application_network_internal") is True and boundary.get("binance_dns_alias") == "local-idp-fixture" and
            boundary.get("backend_certificate_verification") == "existing SSL_CERT_FILE; no bypass" and
            boundary.get("all_running_digests_match") is True, "actual-strict-local-market-boundary")
    require(boundary.get("agent_private_backend_adapter") == "http://idp:18081" and
            boundary.get("adapter_edge_interface_unreachable") is True, "private-agent-backend-adapter")
    rejections = business.get("backend_oidc_rejections", {})
    require(rejections.get("fixture") == "stock-ai-backend-oidc-rejections-v1" and rejections.get("tokens_disclosed") is False and
            rejections.get("external_calls") == 0 and rejections.get("statuses") == {name: 401 for name in
            ("anonymous", "static-user", "expired", "wrong-issuer", "wrong-audience", "wrong-subject")}, "actual-backend-oidc-rejections")
    adapter = business.get("private_backend_adapter", {})
    require(adapter.get("credential_exchange_scoped") is True and adapter.get("account_credentials_preserved") is True, "scoped-credential-exchange")
    for key in ("before", "after"):
        value = adapter.get(key, {})
        require(value.get("fixture") == "stock-ai-private-backend-oidc-adapter-v1" and value.get("external_calls") == 0 and
                value.get("user_token_ttl_seconds") == 45 and value.get("account_credentials_preserved") is True and
                value.get("upstream") == "http://backend:7794" and value.get("privileged_routes") ==
                ["GET /v1/accounts/:id", "GET /v1/market/price"], "private-adapter-provenance")
    for route in ("USER GET /v1/accounts/:id", "ACCOUNT GET /v1/market/price", "ACCOUNT POST /v1/orders"):
        require(adapter["after"].get("routes", {}).get(route, 0) > (adapter["before"].get("routes") or {}).get(route, 0), "private-adapter-route-counters")
    market = business.get("market_provenance", {})
    require(market.get("price") == 100 and market.get("symbol") == "BTCUSDT" and market.get("external_calls") == 0 and
            market.get("ticker_info_ohlcv_public_api") is True, "deterministic-market-responses")
    for key in ("before", "after"):
        require(market.get(key, {}).get("fixture") == "stock-ai-local-binance-v1" and market[key].get("external_calls") == 0, "local-market-counter-identity")
    for route in ("/api/v3/ticker/price", "/api/v3/exchangeInfo", "/api/v3/klines"):
        require(market["after"].get("routes", {}).get(route, 0) > (market["before"].get("routes") or {}).get(route, 0), "actual-market-route-requests")
    require(runtime.get("schema") == "stock-ai.local-release-runtime.v1" and runtime.get("status") == "passed" and
            runtime.get("all_release_bind_sources_persistent") is True and runtime.get("backend_static_user_token") == "disabled", "runtime-boundary")
    require(set(runtime.get("services", {})) == set(images), "complete-runtime-service-set")
    for name, image in images.items():
        require(runtime["services"][name] == {"image": image, "running": True, "health": "healthy"}, "running-healthy-approved-digest")
    require(all(runtime.get("isolation", {}).get(kind, {}).get("disjoint_from_test") is True for kind in ("networks", "volumes", "binds")), "test-release-isolation")
    require(runtime.get("published_ingress") == [{"service": "nginx", "host_ip": "127.0.0.1", "host_port": "8443", "target": "8443/tcp"}], "sole-loopback-ingress")
    require(ingress.get("schema") == "stock-ai.local-release-ingress.v1" and ingress.get("status") == "passed" and
            ingress.get("authenticated_api_status") == 200 and ingress.get("anonymous_api_status") == 401 and
            ingress.get("all_redirects_release_port") is True and ingress.get("test_origin_visited") is False, "strict-ingress-session")
    require(acceptance.get("fingerprints") == rollback and rollback.get("project") == "stock-ai-local-release", "updated-fingerprints")
    for key in ("compose_inputs_sha256", "normalized_compose_sha256", "non_image_env_sha256"):
        require(SHA.fullmatch(rollback.get(key, "")), "fingerprint-digests")
    require(rollback.get("promoted_images") == products, "product-promotion-fingerprint")
    require(all(record.get("promotion_manifest_sha256") == manifest_sha for record in (acceptance, runtime, rollback)), "promotion-binding")
    for filename, key in (("predeployment-backup.json", "predeployment_backup_sha256"), ("accepted-backup.json", "accepted_snapshot_sha256")):
        backup = records[filename]
        require(backup.get("schema") == "stock-ai.complete-release-backup.v1" and backup.get("encrypted") == "AES-256-GCM" and
                backup.get("authenticated_and_inventory_verified") is True and backup.get("all_nine_secrets_and_tls_jwks") is True and
                SHA.fullmatch(backup.get("sha256", "")) and acceptance.get(key) == backup["sha256"], "authenticated-complete-snapshots")
    return {"schema": "stock-ai.t15-evidence-validation.v1", "status": "passed", "all_stores_equal": True,
            "strict_local_market_provenance": True, "business_flow": "passed", "complete_rollback_rehearsed": True,
            "product_images_unchanged": True, "evidence_files": list(FILES)}


def validate(directory, manifest=DEFAULT_MANIFEST):
    require(not directory.is_symlink(), "regular-evidence-directory")
    require({path.name for path in directory.iterdir()} == set(FILES) | {"SHA256SUMS"}, "public-export-only")
    hashes((directory / "SHA256SUMS").read_text(), directory)
    records = {name: json.loads((directory / name).read_text()) for name in FILES}
    images, checksum = approved_images(manifest)
    return validate_records(records, images, checksum)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--manifest", default=DEFAULT_MANIFEST, type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(validate(args.evidence_dir, args.manifest), sort_keys=True))
    except Exception:
        raise SystemExit("T15 sanitized evidence rejected; no completion claimed")
