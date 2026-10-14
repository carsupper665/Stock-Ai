#!/usr/bin/env python3
"""Create private throwaway local-test certificates and environment values."""

import json
import secrets
import subprocess
import base64
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / ".local-test"
CERTS = RUNTIME / "certs"
SECRETS = RUNTIME / "secrets"


def write_secret(name, value):
    path = SECRETS / name
    if path.exists():
        path.chmod(0o444)
        return
    path.write_text(value + "\n", encoding="ascii")
    # Compose file-backed secrets are bind-mounted into uid 65532 containers.
    # The private parent directory keeps the host copy owner-only.
    path.chmod(0o444)


def secret_key():
    return base64.b64encode(secrets.token_bytes(32)).decode("ascii")


def main():
    RUNTIME.mkdir(mode=0o700, exist_ok=True)
    RUNTIME.chmod(0o700)
    CERTS.mkdir(mode=0o755, exist_ok=True)
    CERTS.chmod(0o755)
    SECRETS.mkdir(mode=0o700, exist_ok=True)
    cert = CERTS / "fullchain.pem"
    key = CERTS / "privkey.pem"
    if not cert.exists() or not key.exists():
        subprocess.run(
            [
                "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                "-keyout", str(key), "-out", str(cert), "-days", "2",
                "-subj", "/CN=local-test-fixture",
                "-addext", "subjectAltName=DNS:app.localtest,DNS:idp.localtest",
                "-addext", "basicConstraints=critical,CA:TRUE",
                "-addext", "keyUsage=critical,digitalSignature,keyCertSign,cRLSign",
                "-addext", "extendedKeyUsage=serverAuth",
            ],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    cert.chmod(0o444)
    key.chmod(0o444)
    jwks_key = CERTS / "jwks-signing-key.pem"
    if not jwks_key.exists():
        subprocess.run(
            ["openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", str(jwks_key)],
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    jwks_key.chmod(0o444)

    write_secret("stock-ai-test_backend_user_token", secrets.token_urlsafe(48))
    write_secret("stock-ai-test_backend_account_token_key", secret_key())
    write_secret("stock-ai-test_agent_admin_token", secrets.token_urlsafe(48))
    write_secret("stock-ai-test_llm_admin_token", secrets.token_urlsafe(48))
    write_secret("stock-ai-test_llm_runtime_token", secrets.token_urlsafe(48))
    write_secret("stock-ai-test_llm_master_key", secret_key())
    write_secret("stock-ai-test_oidc_client_secret", "local-test-oidc-client-secret-only")
    write_secret("stock-ai-test_oidc_cookie_key", secret_key())

    references_path = RUNTIME / "registry-references.json"
    if not references_path.is_file():
        raise SystemExit("Missing local registry-references.json; build and digest-pin all five production images first")
    references = json.loads(references_path.read_text(encoding="utf-8"))
    expected = {"backend", "agent-server", "llm-provider-server", "console", "oidc-gateway"}
    digest_ref = re.compile(r"^127\.0\.0\.1:5000/stock-ai/[a-z0-9-]+@sha256:[0-9a-f]{64}$")
    if set(references) != expected or any(not isinstance(value, str) or not digest_ref.fullmatch(value) for value in references.values()):
        raise SystemExit("Local registry references must contain exactly five immutable localhost SHA-256 references")

    values = {
        "STOCK_AI_INSTANCE": "stock-ai-local-test-t10",
        "STOCK_AI_ENVIRONMENT": "Test",
        "STOCK_AI_SECRET_NAMESPACE": "stock-ai-test",
        "STOCK_AI_SECRETS_DIR": str(SECRETS),
        "LOCAL_TEST_CERT_DIR": str(CERTS),
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
    env_path.write_text("".join(f"{key}={value}\n" for key, value in values.items()), encoding="utf-8")
    env_path.chmod(0o600)
    print(f"Prepared isolated local-test files under {RUNTIME}")
    print(f"Compose environment: {env_path}")


if __name__ == "__main__":
    main()
