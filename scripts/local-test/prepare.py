#!/usr/bin/env python3
"""Prepare private local-test files without rotating valid existing material."""

import base64
import json
import os
import re
import secrets
import shutil
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / ".local-test"
CERTS = RUNTIME / "certs"
SECRETS = RUNTIME / "secrets"
CONTAINER_GID = os.getgid()
PRIVATE_FILE_MODE = 0o440
TOKEN_RE = re.compile(rb"^[A-Za-z0-9_-]{64}$")


class PrepareError(RuntimeError):
    pass


def _run(*args: str, stdout=subprocess.PIPE) -> bytes:
    try:
        return subprocess.run(args, check=True, stdout=stdout, stderr=subprocess.DEVNULL).stdout or b""
    except (OSError, subprocess.CalledProcessError) as error:
        raise PrepareError(f"local cryptographic material validation failed ({Path(args[0]).name})") from error


def _secure_directory(path: Path, mode: int, gid: int) -> None:
    if path.is_symlink():
        raise PrepareError(f"refusing symlink directory: {path.name}")
    path.mkdir(parents=True, exist_ok=True, mode=mode)
    if not path.is_dir():
        raise PrepareError(f"expected a directory: {path.name}")
    try:
        os.chown(path, -1, gid)
        path.chmod(mode)
    except OSError as error:
        raise PrepareError(f"cannot set private directory ownership: {path.name}") from error


def _secure_file(path: Path, gid: int) -> None:
    if path.is_symlink() or not path.is_file():
        raise PrepareError(f"expected a regular non-symlink file: {path.name}")
    try:
        os.chown(path, -1, gid)
        path.chmod(PRIVATE_FILE_MODE)
    except OSError as error:
        raise PrepareError(f"cannot set container-readable private file permissions: {path.name}") from error


def _replace_validated(staged: Path, destination: Path, validator, gid: int) -> None:
    validator(staged)
    try:
        os.chown(staged, -1, gid)
        staged.chmod(PRIVATE_FILE_MODE)
        os.replace(staged, destination)
        _secure_file(destination, gid)
    except OSError as error:
        raise PrepareError(f"cannot install local key material: {destination.name}") from error


def _openssl_cert(path: Path) -> None:
    _run("openssl", "x509", "-in", str(path), "-noout", "-checkhost", "app.localtest")
    _run("openssl", "x509", "-in", str(path), "-noout", "-checkhost", "idp.localtest")
    _run("openssl", "x509", "-in", str(path), "-noout", "-checkend", "0")


def _openssl_private_key(path: Path) -> None:
    _run("openssl", "pkey", "-in", str(path), "-check", "-noout")
    details = _run("openssl", "pkey", "-in", str(path), "-text", "-noout")
    if b"private-key: (2048 bit" not in details.lower():
        raise PrepareError(f"private key has an unexpected algorithm or size: {path.name}")


def _validate_tls_pair(cert: Path, key: Path) -> None:
    _openssl_cert(cert)
    _openssl_private_key(key)
    cert_public = _run("openssl", "x509", "-in", str(cert), "-pubkey", "-noout")
    key_public = _run("openssl", "pkey", "-in", str(key), "-pubout")
    if cert_public.strip() != key_public.strip():
        raise PrepareError("local TLS certificate and private key do not match")


def _ensure_tls_pair(cert: Path, key: Path, gid: int) -> None:
    cert_exists, key_exists = cert.exists() or cert.is_symlink(), key.exists() or key.is_symlink()
    if cert_exists != key_exists:
        raise PrepareError("partial TLS certificate/key state; refusing to rotate existing material")
    if cert_exists:
        _validate_tls_pair(cert, key)
        _secure_file(cert, gid)
        _secure_file(key, gid)
        return

    with tempfile.TemporaryDirectory(prefix="tls-stage-", dir=CERTS) as temporary:
        stage = Path(temporary)
        staged_cert, staged_key = stage / "fullchain.pem", stage / "privkey.pem"
        _run(
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(staged_key), "-out", str(staged_cert), "-days", "2",
            "-subj", "/CN=local-test-fixture",
            "-addext", "subjectAltName=DNS:app.localtest,DNS:idp.localtest",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,digitalSignature,keyCertSign,cRLSign",
            "-addext", "extendedKeyUsage=serverAuth",
        )
        _validate_tls_pair(staged_cert, staged_key)
        os.replace(staged_cert, cert)
        os.replace(staged_key, key)
    _secure_file(cert, gid)
    _secure_file(key, gid)


def _ensure_jwks_key(path: Path, gid: int) -> None:
    if path.exists() or path.is_symlink():
        _openssl_private_key(path)
        _secure_file(path, gid)
        return
    with tempfile.TemporaryDirectory(prefix="jwks-stage-", dir=CERTS) as temporary:
        staged = Path(temporary) / path.name
        _run("openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", str(staged), stdout=subprocess.DEVNULL)
        _openssl_private_key(staged)
        _replace_validated(staged, path, _openssl_private_key, gid)


def _valid_token(contents: bytes) -> bool:
    return bool(TOKEN_RE.fullmatch(contents))


def _valid_base64_key(contents: bytes) -> bool:
    try:
        return len(base64.b64decode(contents, validate=True)) == 32
    except (ValueError, base64.binascii.Error):
        return False


def _secret_value(name: str) -> tuple[bytes, object]:
    if name == "stock-ai-test_oidc_client_secret":
        return b"local-test-oidc-client-secret-only\n", lambda data: data == b"local-test-oidc-client-secret-only"
    if name in {"stock-ai-test_backend_account_token_key", "stock-ai-test_llm_master_key", "stock-ai-test_oidc_cookie_key"}:
        return (base64.b64encode(secrets.token_bytes(32)) + b"\n", _valid_base64_key)
    return (secrets.token_urlsafe(48).encode("ascii") + b"\n", _valid_token)


def _write_secret(name: str, gid: int) -> None:
    path = SECRETS / name
    generated, validator = _secret_value(name)
    if path.exists() or path.is_symlink():
        if path.is_symlink() or not path.is_file():
            raise PrepareError(f"invalid existing secret file: {name}")
        original = path.read_bytes()
        value = original[:-1] if original.endswith(b"\n") else original
        if not validator(value):
            raise PrepareError(f"malformed existing secret file: {name}")
        _secure_file(path, gid)
        return
    value = generated[:-1]
    if not validator(value):
        raise PrepareError(f"generated secret format is invalid: {name}")
    fd, temporary = tempfile.mkstemp(prefix=f".{name}.", dir=SECRETS)
    staged = Path(temporary)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(generated)
        _secure_file(staged, gid)
        os.replace(staged, path)
    finally:
        staged.unlink(missing_ok=True)


def prepare_material() -> None:
    gid = os.getgid()
    _secure_directory(RUNTIME, 0o700, gid)
    _secure_directory(CERTS, 0o750, gid)
    _secure_directory(SECRETS, 0o700, gid)
    _ensure_tls_pair(CERTS / "fullchain.pem", CERTS / "privkey.pem", gid)
    _ensure_jwks_key(CERTS / "jwks-signing-key.pem", gid)
    for name in (
        "stock-ai-test_backend_user_token",
        "stock-ai-test_backend_account_token_key",
        "stock-ai-test_agent_admin_token",
        "stock-ai-test_llm_admin_token",
        "stock-ai-test_llm_runtime_token",
        "stock-ai-test_llm_master_key",
        "stock-ai-test_oidc_client_secret",
        "stock-ai-test_oidc_cookie_key",
    ):
        _write_secret(name, gid)


def main() -> None:
    prepare_material()
    references_path = RUNTIME / "registry-references.json"
    if not references_path.is_file() or references_path.is_symlink():
        raise PrepareError("missing local registry-references.json; build and digest-pin all five production images first")
    references = json.loads(references_path.read_text(encoding="utf-8"))
    expected = {"backend", "agent-server", "llm-provider-server", "console", "oidc-gateway"}
    digest_ref = re.compile(r"^127\.0\.0\.1:5000/stock-ai/[a-z0-9-]+@sha256:[0-9a-f]{64}$")
    if set(references) != expected or any(not isinstance(value, str) or not digest_ref.fullmatch(value) for value in references.values()):
        raise PrepareError("local registry references must contain exactly five immutable localhost SHA-256 references")

    values = {
        "STOCK_AI_INSTANCE": "stock-ai-local-test-t10",
        "STOCK_AI_ENVIRONMENT": "Test",
        "STOCK_AI_SECRET_NAMESPACE": "stock-ai-test",
        "STOCK_AI_SECRETS_DIR": str(SECRETS),
        "LOCAL_TEST_CERT_DIR": str(CERTS),
        "LOCAL_TEST_GID": str(os.getgid()),
        "BACKEND_IMAGE": references["backend"],
        "LLM_PROVIDER_SERVER_IMAGE": references["llm-provider-server"],
        "AGENT_SERVER_IMAGE": references["agent-server"],
        "CONSOLE_IMAGE": references["console"],
        "OIDC_GATEWAY_IMAGE": references["oidc-gateway"],
        "NGINX_IMAGE": "nginx@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10",
        "TEST_APP_HOST": "app.localtest",
        "TEST_BIND_ADDRESS": "127.0.0.1",
        "TEST_TLS_DIR": str(CERTS),
        "OIDC_ISSUER": "https://idp.localtest:9443/realms/stock-ai-test",
        "OIDC_AUTHORIZATION_URL": "https://idp.localtest:9443/realms/stock-ai-test/authorize",
        "OIDC_TOKEN_URL": "https://idp.localtest:9443/realms/stock-ai-test/token",
        "OIDC_JWKS_URL": "https://idp.localtest:9443/realms/stock-ai-test/jwks",
        "OIDC_END_SESSION_URL": "https://idp.localtest:9443/realms/stock-ai-test/logout",
        "OIDC_AUDIENCE": "stock-ai-api-test",
        "OIDC_USER_SUBJECT": "local-test-user",
        "ALLOW_STATIC_USER_TOKEN": "false",
    }
    env_path = RUNTIME / "compose.env"
    temporary = RUNTIME / ".compose.env.tmp"
    temporary.write_text("".join(f"{key}={value}\n" for key, value in values.items()), encoding="utf-8")
    temporary.chmod(0o600)
    os.replace(temporary, env_path)
    env_path.chmod(0o600)
    print(f"Prepared isolated local-test files under {RUNTIME}")
    print(f"Compose environment: {env_path}")


if __name__ == "__main__":
    try:
        main()
    except (PrepareError, OSError, json.JSONDecodeError) as error:
        raise SystemExit(str(error)) from error
