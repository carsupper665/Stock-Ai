#!/usr/bin/env python3
"""Generate isolated private Release material and consume certified image digests."""

import argparse
import hashlib
import importlib.util
import json
import os
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from scripts.local_operations import DIGEST_RE, Operator, WorkflowError

spec = importlib.util.spec_from_file_location("private_material", ROOT / "scripts/local-test/prepare.py")
material = importlib.util.module_from_spec(spec)
spec.loader.exec_module(material)

INSTANCE = "stock-ai-local-release"
NAMESPACE = "stock-ai-release"
HOST = "app.localrelease:8443"
ISSUER = f"https://{HOST}/realms/{NAMESPACE}"
SUFFIXES = ("backend_user_token", "backend_account_token_key", "agent_admin_token",
            "llm_admin_token", "llm_runtime_token", "llm_master_key", "oidc_client_secret",
            "oidc_cookie_key", "fixture_provider_secret")
DEFAULT_MANIFEST = ROOT / "deploy/evidence/local-test/promotion/promotion.json"
AUXILIARY = ROOT / "deploy/local-release/auxiliary-images.json"


def persistent_runtime(path):
    path = path.resolve()
    if "worktrees" in path.parts or path == ROOT or ROOT in path.parents:
        raise WorkflowError("Release runtime must be outside disposable worktrees")
    return path


def approved_images(manifest):
    # Reuse the full operator contract, including all package artifact hashes.
    promoted, checksum = Operator.__new__(Operator).promotion(
        manifest, {"services": {"oidc-gateway": {}}})
    auxiliary = json.loads(AUXILIARY.read_text())
    if auxiliary.get("schema") != "stock-ai.local-auxiliary-images.v1":
        raise WorkflowError("Auxiliary image approval schema is invalid.")
    for name in ("nginx", "idp"):
        entry = auxiliary.get(name, {})
        if entry.get("status") != "tested" or not isinstance(entry.get("image"), str) or not DIGEST_RE.fullmatch(entry["image"]):
            raise WorkflowError("Auxiliary image approval is missing.")
        promoted[name] = entry["image"]
    sources = auxiliary["idp"].get("source_sha256", {})
    if set(sources) != {"scripts/local-test/fixture.go", "scripts/local-test/Dockerfile", "scripts/local-test/go.mod"}:
        raise WorkflowError("Auxiliary fixture source approval is missing.")
    for name, expected in sources.items():
        if hashlib.sha256((ROOT / name).read_bytes()).hexdigest() != expected:
            raise WorkflowError("Fixture source differs from tested immutable auxiliary image.")
    return promoted, checksum


def fixture_config():
    return {
        "issuer": ISSUER, "client_id": NAMESPACE, "audience": "stock-ai-api-release",
        "subject": "local-release-user", "redirect_uri": f"https://{HOST}/oauth2/callback",
        "logout_uri": f"https://{HOST}/", "key_id": "local-release-signing-key",
        "client_secret_file": f"/run/secrets/{NAMESPACE}_oidc_client_secret",
        "provider_secret_file": f"/run/secrets/{NAMESPACE}_fixture_provider_secret",
        "market_fixture": True,
        "backend_adapter": True,
        "backend_secret_file": f"/run/secrets/{NAMESPACE}_backend_user_token",
    }


def install_private(path, content, gid, preserve=False):
    if path.is_symlink():
        raise material.PrepareError("Refusing symlink private configuration.")
    if path.exists() and preserve:
        if path.read_bytes() != content:
            raise material.PrepareError("Existing fixture configuration differs; refusing replacement.")
        material._secure_file(path, gid)
        return
    staged = path.with_name("." + path.name + ".tmp")
    if staged.exists() or staged.is_symlink():
        raise material.PrepareError("Private staging path already exists.")
    fd = os.open(staged, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(content)
        material._secure_file(staged, gid)
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def prepare(manifest=DEFAULT_MANIFEST, runtime=None):
    images, checksum = approved_images(manifest)
    if runtime is None:
        raise WorkflowError("An explicit persistent Release runtime is required")
    runtime = persistent_runtime(runtime)
    certs, secrets = runtime / "certs", runtime / "secrets"
    gid = os.getgid()
    for path, mode in ((runtime, 0o700), (certs, 0o750), (secrets, 0o700)):
        material._secure_directory(path, mode, gid)
    material._ensure_tls_pair(certs / "fullchain.pem", certs / "privkey.pem", gid, ("app.localrelease", "api.binance.com"))
    material._ensure_jwks_key(certs / "jwks-signing-key.pem", gid)
    for suffix in SUFFIXES:
        material._write_secret(f"{NAMESPACE}_{suffix}", gid, secrets)
    install_private(certs / "config.json", (json.dumps(fixture_config(), indent=2) + "\n").encode(), gid, True)
    values = {
        "STOCK_AI_INSTANCE": INSTANCE, "STOCK_AI_ENVIRONMENT": "Release",
        "STOCK_AI_SECRET_NAMESPACE": NAMESPACE, "STOCK_AI_SECRETS_DIR": str(secrets.resolve()),
        "RELEASE_TLS_DIR": str(certs.resolve()), "LOCAL_RELEASE_GID": str(gid),
        "RELEASE_APP_HOST": HOST, "OIDC_ISSUER": ISSUER, "OIDC_AUDIENCE": "stock-ai-api-release",
        "OIDC_USER_SUBJECT": "local-release-user", "ALLOW_STATIC_USER_TOKEN": "false",
        "PROMOTION_MANIFEST_SHA256": checksum,
        "AUXILIARY_IMAGES_SHA256": hashlib.sha256(AUXILIARY.read_bytes()).hexdigest(),
    }
    for suffix, endpoint in (("AUTHORIZATION", "authorize"), ("TOKEN", "token"),
                             ("JWKS", "jwks"), ("END_SESSION", "logout")):
        values[f"OIDC_{suffix}_URL"] = f"{ISSUER}/{endpoint}"
    for service, variable in (("backend", "BACKEND_IMAGE"), ("agent-server", "AGENT_SERVER_IMAGE"),
                              ("llm-provider-server", "LLM_PROVIDER_SERVER_IMAGE"), ("console", "CONSOLE_IMAGE"),
                              ("oidc-gateway", "OIDC_GATEWAY_IMAGE"), ("nginx", "NGINX_IMAGE"),
                              ("idp", "LOCAL_RELEASE_FIXTURE_IMAGE")):
        values[variable] = images[service]
    install_private(runtime / "compose.env", "".join(f"{key}={value}\n" for key, value in values.items()).encode(), gid)
    (runtime / "compose.env").chmod(0o600)
    return runtime / "compose.env"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--runtime", type=Path, required=True,
                        help="Persistent private directory outside disposable worktrees")
    args = parser.parse_args()
    print(f"Prepared private local-release environment: {prepare(args.manifest, args.runtime.resolve())}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, WorkflowError, material.PrepareError):
        raise SystemExit("Release preparation failed; existing material was not rotated. Check promotion and private files.")
