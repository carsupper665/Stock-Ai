#!/usr/bin/env python3
"""Fail closed before Release startup; never emit rendered config or secret contents."""

import argparse
import hashlib
import json
import re
from pathlib import Path

from prepare import (AUXILIARY, DEFAULT_MANIFEST, HOST, INSTANCE, ISSUER, NAMESPACE,
                     ROOT, SUFFIXES, approved_images, fixture_config, material)
from scripts.local_operations import Operator, WorkflowError

FILES = [ROOT / "deploy/compose/compose.yaml", ROOT / "deploy/compose/compose.local-release.yaml"]
SERVICES = {"backend", "agent-server", "llm-provider-server", "console", "oidc-gateway", "nginx", "idp"}
DIGEST = re.compile(r"^[^\s@]+@sha256:[0-9a-f]{64}$")
FORBIDDEN = re.compile(r"local[-_.]?test|stock-ai-(?:api-)?test|__Host-stock-ai-test|placeholder|changeme|change-me|replace-me|example\.com", re.I)


def strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for entry in value.values():
            yield from strings(entry)
    elif isinstance(value, list):
        for entry in value:
            yield from strings(entry)


def validate(config, values, images):
    if not config.get("networks", {}).get("application", {}).get("internal"):
        raise WorkflowError("Release market application network must block external routing.")
    if "api.binance.com" not in config.get("services", {}).get("idp", {}).get("networks", {}).get("application", {}).get("aliases", []):
        raise WorkflowError("Release market hostname must resolve to its local fixture.")
    if config.get("services", {}).get("agent-server", {}).get("environment", {}).get("AGENT_SERVER_BACKEND_URL") != "http://idp:18081":
        raise WorkflowError("Immutable Agent must use the private scoped Backend adapter.")
    if any(FORBIDDEN.search(value) or "${" in value for value in strings(config)):
        raise WorkflowError("Test identifiers, placeholders or unresolved variables detected.")
    if config.get("name") != INSTANCE or values.get("STOCK_AI_INSTANCE") != INSTANCE:
        raise WorkflowError("Release project identity is invalid.")
    if values.get("STOCK_AI_ENVIRONMENT") != "Release" or values.get("STOCK_AI_SECRET_NAMESPACE") != NAMESPACE:
        raise WorkflowError("Release environment or secret namespace is invalid.")
    services = config.get("services", {})
    if set(services) != SERVICES:
        raise WorkflowError("Release service set is invalid.")
    for name, service in services.items():
        if "build" in service or service.get("network_mode") or service.get("privileged"):
            raise WorkflowError("Build directives or unsupported network/privilege mode detected.")
        ref = service.get("image", "")
        if not DIGEST.fullmatch(ref) or ref != images[name]:
            raise WorkflowError("Mutable or unapproved image detected.")
        ports = service.get("ports", [])
        if name == "nginx":
            if len(ports) != 1 or any(str(ports[0].get(key, "")) != value for key, value in
                                      (("host_ip", "127.0.0.1"), ("published", "8443"), ("target", "8443"), ("protocol", "tcp"))):
                raise WorkflowError("Nginx must publish only 127.0.0.1:8443 TCP.")
        elif ports:
            raise WorkflowError("An internal service publishes a host port.")
        environment = service.get("environment", {})
        if name != "idp" and name != "nginx" and environment.get("STOCK_AI_ENVIRONMENT") != "Release":
            raise WorkflowError("Service environment identity is invalid.")
        if name in ("backend", "console", "oidc-gateway"):
            if environment.get("OIDC_ISSUER") != ISSUER or environment.get("OIDC_USER_SUBJECT") != "local-release-user":
                raise WorkflowError("Release issuer or subject is invalid.")
        if name in ("backend", "console") and environment.get("OIDC_AUDIENCE") != "stock-ai-api-release":
            raise WorkflowError("Release audience is invalid.")
        expected_networks = {"edge"} if name in ("nginx", "oidc-gateway") else (
            {"edge", "application"} if name in ("idp", "console") else {"application"})
        if set(service.get("networks", {})) != expected_networks:
            raise WorkflowError("Private service network boundary is invalid.")
    gateway = services["oidc-gateway"]["environment"]
    if services["backend"]["environment"].get("ALLOW_STATIC_USER_TOKEN") != "false" or values.get("ALLOW_STATIC_USER_TOKEN") != "false":
        raise WorkflowError("Release static user-token authentication must be disabled.")
    if services["oidc-gateway"].get("user") != f"65532:{values.get('LOCAL_RELEASE_GID')}":
        raise WorkflowError("Gateway must use the private Release secret group.")
    for key, value in {"OIDC_CLIENT_ID": NAMESPACE, "OIDC_COOKIE_NAME": "__Host-stock-ai-release-session",
                       "OIDC_REDIRECT_URI": f"https://{HOST}/oauth2/callback", "OIDC_POST_LOGOUT_URI": f"https://{HOST}/",
                       "OIDC_AUTHORIZATION_URL": ISSUER+"/authorize", "OIDC_TOKEN_URL": ISSUER+"/token",
                       "OIDC_JWKS_URL": ISSUER+"/jwks", "OIDC_END_SESSION_URL": ISSUER+"/logout"}.items():
        if gateway.get(key) != value:
            raise WorkflowError("Release client, cookie or endpoint identity is invalid.")
    for kind, names in (("networks", ("application", "edge")),
                        ("volumes", ("backend_state", "agent_state", "llm_state"))):
        entries = config.get(kind, {})
        if set(entries) != set(names):
            raise WorkflowError("Release persistence or network set is missing.")
        for name in names:
            entry = entries[name]
            if entry.get("name") != f"{INSTANCE}_{name}" or entry.get("external"):
                raise WorkflowError("Release persistence or network identity is invalid.")
    for service, volume, path_key, path in (("backend", "backend_state", "DB_DSN", "/data/backend.db"),
                                           ("agent-server", "agent_state", "AGENT_SERVER_DB_PATH", "/data/agent-server.db"),
                                           ("llm-provider-server", "llm_state", "LLM_SERVER_DB_PATH", "/data/llm-provider.db")):
        mounts = services[service].get("volumes", [])
        data = [mount for mount in mounts if mount.get("target") == "/data"]
        if len(data) != 1 or data[0].get("type") != "volume" or data[0].get("source") != volume or data[0].get("read_only"):
            raise WorkflowError("Required writable database persistence is missing.")
        if services[service]["environment"].get(path_key) != path:
            raise WorkflowError("Database path escapes persistent storage.")
    if services["backend"]["environment"].get("LOG_DIR") != "/data/logs" or services["agent-server"]["environment"].get("AGENT_SERVER_HISTORY_DIR") != "/data/history":
        raise WorkflowError("Required logs/history persistence is missing.")


def validate_material(config, values, runtime, deployment_root=ROOT):
    certs, secrets = runtime / "certs", runtime / "secrets"
    if values.get("STOCK_AI_SECRETS_DIR") != str(secrets) or values.get("RELEASE_TLS_DIR") != str(certs):
        raise WorkflowError("Release material must use its own private runtime directory.")
    for directory, allowed in ((runtime, 0o700), (secrets, 0o700), (certs, 0o750)):
        if directory.is_symlink() or not directory.is_dir() or directory.stat().st_mode & 0o777 != allowed:
            raise WorkflowError("Private Release directory permissions are invalid.")
    expected_files = {f"{NAMESPACE}_{suffix}" for suffix in SUFFIXES}
    definitions = config.get("secrets", {})
    if {Path(entry.get("file", "")).name for entry in definitions.values()} != expected_files:
        raise WorkflowError("Release secret definitions are missing or unexpected.")
    for entry in definitions.values():
        path = Path(entry.get("file", ""))
        if path.parent != secrets or path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o777 != 0o440:
            raise WorkflowError("Release secret location or permissions are invalid.")
        value = path.read_bytes().strip()
        if FORBIDDEN.search(value.decode("ascii")):
            raise WorkflowError("Placeholder or Test secret detected.")
        validator = material._valid_base64_key if path.name.endswith(("_account_token_key", "_master_key", "_cookie_key")) else material._valid_token
        if not validator(value) or len(set(value)) < 8:
            raise WorkflowError("Release secret format or entropy is invalid.")
    for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem", "config.json"):
        path = certs / name
        if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o777 != 0o440:
            raise WorkflowError("Release fixture material permissions are invalid.")
    if json.loads((certs / "config.json").read_text()) != fixture_config():
        raise WorkflowError("Release fixture identity policy differs from gateway and API policy.")
    material._validate_tls_pair(certs / "fullchain.pem", certs / "privkey.pem", ("app.localrelease", "api.binance.com"))
    material._openssl_private_key(certs / "jwks-signing-key.pem")
    required = {
        "backend": {"backend_user_token", "backend_account_token_key"},
        "agent-server": {"agent_admin_token", "backend_user_token", "llm_runtime_token"},
        "llm-provider-server": {"llm_admin_token", "llm_runtime_token", "llm_master_key"},
        "console": {"agent_admin_token", "llm_admin_token", "llm_runtime_token"},
        "oidc-gateway": {"oidc_client_secret", "oidc_cookie_key"},
        "idp": {"oidc_client_secret", "fixture_provider_secret", "backend_user_token"}, "nginx": set(),
    }
    for name, service in config["services"].items():
        targets = {mount.get("target") for mount in service.get("secrets", [])}
        if targets != {f"{NAMESPACE}_{suffix}" for suffix in required[name]}:
            raise WorkflowError("Required service secret mounts are missing or unexpected.")
        for key, value in service.get("environment", {}).items():
            if key.endswith("_FILE") and value.startswith("/run/secrets/") and value.removeprefix("/run/secrets/") not in targets:
                raise WorkflowError("Service secret-file setting has no private mount.")
        for mount in service.get("secrets", []):
            source = mount.get("source")
            if source not in definitions or mount.get("target") != Path(definitions[source]["file"]).name:
                raise WorkflowError("Release secret target namespace is invalid.")
        for mount in service.get("volumes", []):
            if mount.get("type") == "bind":
                source = Path(mount.get("source", ""))
                if not source.exists() or source.is_symlink() or not mount.get("read_only"):
                    raise WorkflowError("Release bind mount must exist and be read-only.")
                if source != certs and source.parent != certs and source not in (
                    deployment_root / "deploy/nginx/release-nginx.conf", deployment_root / "deploy/nginx/release-edge.conf.template"):
                    raise WorkflowError("Unexpected Release bind mount detected.")


def lint(env_file, manifest, files=FILES):
    runtime = env_file.absolute().parent
    if env_file.is_symlink() or env_file.stat().st_mode & 0o777 != 0o600:
        raise WorkflowError("Release env file must be private and regular.")
    operator = Operator(env_file, files, runtime / "evidence", 60)
    for path in operator.compose_files:
        if re.search(r"^\s*build\s*:", path.read_text(), re.M):
            raise WorkflowError("Release Compose inputs contain a build directive.")
    images, checksum = approved_images(manifest)
    if operator.values.get("PROMOTION_MANIFEST_SHA256") != checksum:
        raise WorkflowError("Promotion manifest differs from prepared Release approval.")
    if operator.values.get("AUXILIARY_IMAGES_SHA256") != hashlib.sha256(AUXILIARY.read_bytes()).hexdigest():
        raise WorkflowError("Auxiliary approval differs from prepared Release approval.")
    config = operator.compose_config()
    validate(config, operator.values, images)
    validate_material(config, operator.values, runtime, operator.compose_files[0].parents[2])
    return {"schema": "stock-ai.local-release-lint.v1", "status": "passed", "project": INSTANCE,
            "promotion_manifest_sha256": checksum, "services": sorted(SERVICES),
            "published_ingress": "127.0.0.1:8443/tcp", "image_policy": "exact-approved-digests-no-build"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=ROOT / ".local-release/compose.env")
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--compose-file", type=Path, action="append")
    parser.add_argument("--evidence", type=Path)
    args = parser.parse_args()
    result = lint(args.env_file, args.manifest, args.compose_file or FILES)
    if args.evidence:
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, WorkflowError, material.PrepareError):
        raise SystemExit("Release lint failed closed. Check private material, isolation, ingress, persistence and image approvals.")
