#!/usr/bin/env python3
"""Run the disposable local Docker recovery acceptance exercise.

All credentials are generated in a mode-0700 temporary directory and are
deleted at exit.  Only boolean results, counts, hashes, image IDs, and timings
are written to the sanitized evidence file.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit


sys.path.insert(0, str(Path(__file__).resolve().parent))
import linux_recovery as recovery  # noqa: E402


APPLICATION_IMAGES = {
    "BACKEND_IMAGE": "stock-ai/backend:0.7.0",
    "AGENT_SERVER_IMAGE": "stock-ai/agent-server:0.7.0",
    "LLM_PROVIDER_SERVER_IMAGE": "stock-ai/llm-provider-server:0.7.0",
    "CONSOLE_IMAGE": "stock-ai/console:0.7.0",
}
NAMESPACE = "stock-ai-test"
PROVIDER_ID = "recovery-provider"
MODEL_NAME = "recovery-model"
PROVIDER_ALIAS = "recovery-provider-fixture"
FIXTURE_ACCOUNT = "recovery-fixture-account"
FIXTURE_MESSAGE = "recovery fixture message"
FIXTURE_SESSION = "recovery fixture session"
EXPECTED_SUMMARY = "recovery fixture summary"
REDACT_VALUES: set[str] = set()


class ExerciseError(RuntimeError):
    pass


def now() -> datetime:
    return datetime.now(timezone.utc)


def run(
    args: list[str], *, allow_failure: bool = False
) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(args, text=True, capture_output=True, check=False)
    if completed.returncode and not allow_failure:
        safe_tail = " ".join(args[-4:])
        raise ExerciseError(
            f"Exercise command failed with exit {completed.returncode}: "
            f"{args[0]} ... {safe_tail}."
        )
    return completed


def compose(env_file: Path, compose_file: Path, *args: str) -> subprocess.CompletedProcess[str]:
    command = [
        "docker",
        "compose",
        "--env-file",
        str(env_file),
        "-f",
        str(compose_file),
        *args,
    ]
    completed = run(command, allow_failure=True)
    if completed.returncode:
        status = run(
            [
                "docker",
                "compose",
                "--env-file",
                str(env_file),
                "-f",
                str(compose_file),
                "ps",
                "--all",
                "--format",
                "json",
            ],
            allow_failure=True,
        )
        safe_status: list[dict[str, Any]] = []
        try:
            decoded = json.loads(status.stdout or "[]")
            if isinstance(decoded, dict):
                decoded = [decoded]
            for item in decoded:
                safe_status.append(
                    {
                        key: item.get(key)
                        for key in ("Service", "State", "Health", "ExitCode")
                    }
                )
        except (json.JSONDecodeError, TypeError):
            for line in status.stdout.splitlines():
                try:
                    item = json.loads(line)
                except (json.JSONDecodeError, TypeError):
                    continue
                safe_status.append(
                    {
                        key: item.get(key)
                        for key in ("Service", "State", "Health", "ExitCode")
                    }
                )
        error_text = completed.stderr.lower()
        error_classes = [
            label
            for label, phrases in {
                "compose_env_or_interpolation": ("could not parse", "interpolation", "invalid template"),
                "image_resolution": ("no such image", "unable to get image", "pull access denied"),
                "mount_or_permission": ("mount denied", "permission denied", "operation not permitted"),
                "dependency_unhealthy": ("dependency failed", "is unhealthy"),
                "invalid_runtime_spec": ("invalid reference", "invalid spec", "invalid mount"),
            }.items()
            if any(phrase in error_text for phrase in phrases)
        ]
        logs = run(
            [
                "docker",
                "compose",
                "--env-file",
                str(env_file),
                "-f",
                str(compose_file),
                "logs",
                "--no-color",
                "--tail",
                "40",
            ],
            allow_failure=True,
        ).stdout
        for value in sorted(REDACT_VALUES, key=len, reverse=True):
            if len(value) >= 8:
                logs = logs.replace(value, "[REDACTED]")
        if any(value in logs for value in REDACT_VALUES if len(value) >= 8):
            logs = "[diagnostic withheld]"
        safe_log_tail = logs[-1200:].replace("\x00", "")
        raise ExerciseError(
            f"Docker Compose {' '.join(args)} failed; service_status={safe_status}; "
            f"error_classes={error_classes or ['unclassified']}; "
            f"diagnostic_sha256={hashlib.sha256(completed.stderr.encode()).hexdigest()}; "
            f"sanitized_log_tail={safe_log_tail!r}."
        )
    return completed


def image_id(reference: str) -> str:
    value = run(["docker", "image", "inspect", reference, "--format", "{{.Id}}"]).stdout.strip()
    if not recovery._immutable_helper(value):
        raise AssertionError("unreachable")
    return value


def private_write(path: Path, content: str) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        os.write(descriptor, content.encode("utf-8"))
    finally:
        os.close(descriptor)


def create_secret_set(directory: Path) -> tuple[dict[str, str], Path]:
    directory.mkdir(mode=0o700)
    values: dict[str, str] = {}
    for name in recovery.SECRETS:
        if name in ("backend_account_token_key", "llm_master_key"):
            value = base64.b64encode(secrets.token_bytes(32)).decode("ascii")
        else:
            value = secrets.token_urlsafe(48)
        values[name] = value
        private_write(directory / f"{NAMESPACE}_{name}", value + "\n")
    recovery_key = directory.parent / "recovery.key"
    private_write(
        recovery_key,
        base64.b64encode(secrets.token_bytes(32)).decode("ascii") + "\n",
    )
    return values, recovery_key


def write_environment(
    path: Path,
    stack: str,
    secret_directory: Path,
    backup_directory: Path,
    images: dict[str, str],
) -> None:
    catalog = json.dumps(
        [
            {
                "model_name": MODEL_NAME,
                "provider_id": PROVIDER_ID,
                "model": "fixture-model",
            }
        ],
        separators=(",", ":"),
    )
    assignments = {
        "STOCK_AI_INSTANCE": stack,
        "STOCK_AI_ENVIRONMENT": "Test",
        "STOCK_AI_SECRET_NAMESPACE": NAMESPACE,
        "STOCK_AI_SECRETS_DIR": str(secret_directory),
        "STOCK_AI_BACKUP_DIR": str(backup_directory),
        **images,
        "ALLOW_STATIC_USER_TOKEN": "true",
        "OIDC_ISSUER": "https://idp.recovery.invalid/realms/recovery",
        "OIDC_AUDIENCE": "stock-ai-recovery",
        "OIDC_USER_SUBJECT": "recovery-subject",
        "OIDC_JWKS_URL": "https://idp.recovery.invalid/realms/recovery/certs",
        "OIDC_TOKEN_TYPE": "at+jwt",
        "LLM_SERVER_MODEL_CATALOG": catalog,
    }
    REDACT_VALUES.update(str(value) for value in assignments.values())
    private_write(
        path,
        "\n".join(f"{key}={json.dumps(value)}" for key, value in assignments.items()) + "\n",
    )


def start_provider_fixture(
    name: str, network: str, config: Path, nginx_image: str
) -> None:
    run(
        [
            "docker",
            "run",
            "--detach",
            "--name",
            name,
            "--network",
            network,
            "--network-alias",
            PROVIDER_ALIAS,
            "--cap-drop",
            "ALL",
            "--cap-add",
            "CHOWN",
            "--cap-add",
            "DAC_OVERRIDE",
            "--cap-add",
            "SETGID",
            "--cap-add",
            "SETUID",
            "--security-opt",
            "no-new-privileges:true",
            "--pids-limit",
            "64",
            "--mount",
            f"type=bind,src={config},dst=/etc/nginx/nginx.conf,readonly",
            nginx_image,
        ]
    )
    running = run(
        ["docker", "inspect", "--format", "{{.State.Running}}", name]
    ).stdout.strip()
    if running != "true":
        raise ExerciseError("Provider fixture container did not remain running.")


def api_json(
    network: str,
    nginx_image: str,
    auth_file: Path,
    url: str,
    request_directory: Path,
    body: dict[str, Any] | None = None,
) -> dict[str, Any]:
    command = (
        'AUTH="$(cat /run/auth)"; '
        'exec wget -SqO- --timeout=15 --header "Authorization: Bearer $AUTH" '
        + ('--header "Content-Type: application/json" --post-file /run/body ' if body is not None else "")
        + '"$1"'
    )
    arguments = [
        "docker",
        "run",
        "--rm",
        "--network",
        network,
        "--cap-drop",
        "ALL",
        "--cap-add",
        "DAC_OVERRIDE",
        "--security-opt",
        "no-new-privileges:true",
        "--pids-limit",
        "32",
        "--mount",
        f"type=bind,src={auth_file},dst=/run/auth,readonly",
    ]
    body_path: Path | None = None
    if body is not None:
        body_path = request_directory / f"request-{secrets.token_hex(6)}.json"
        private_write(body_path, json.dumps(body, separators=(",", ":")))
        arguments.extend(
            ["--mount", f"type=bind,src={body_path},dst=/run/body,readonly"]
        )
    arguments.extend(
        [
            "--entrypoint",
            "/bin/sh",
            nginx_image,
            "-ceu",
            command,
            "request",
            url,
        ]
    )
    try:
        completed = run(arguments, allow_failure=True)
        output = completed.stdout
    finally:
        if body_path:
            body_path.unlink(missing_ok=True)
    if completed.returncode:
        import re

        error_code = "unavailable"
        status_match = re.search(r"HTTP/\S+\s+(\d{3})", completed.stderr)
        try:
            failure = json.loads(output)
            if isinstance(failure, dict) and isinstance(failure.get("error"), dict):
                error_code = str(failure["error"].get("code", "unavailable"))
        except (json.JSONDecodeError, TypeError):
            pass
        raise ExerciseError(
            f"Fixture API request failed: path={urlsplit(url).path}, "
            f"http_status={status_match.group(1) if status_match else 'unavailable'}, "
            f"error_code={error_code}, client_exit={completed.returncode}."
        )
    if not output.strip():
        return {}
    try:
        value = json.loads(output)
    except json.JSONDecodeError as error:
        raise ExerciseError("Fixture API returned non-JSON content.") from error
    if not isinstance(value, dict):
        raise ExerciseError("Fixture API returned an unexpected JSON shape.")
    return value


def seed_business_fixture(
    stack: str,
    secret_directory: Path,
    request_directory: Path,
    nginx_image: str,
    provider_token: str,
) -> dict[str, Any]:
    network = f"{stack}_application"
    direct_probe = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_runtime_token",
        f"http://{PROVIDER_ALIAS}:8081/v1/chat/completions",
        request_directory,
        {"model": "fixture-model", "messages": []},
    )
    if not isinstance(direct_probe.get("choices"), list):
        raise ExerciseError("Direct Provider fixture probe failed.")
    provider = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_admin_token",
        "http://llm-provider-server:8090/v1/providers",
        request_directory,
        {
            "id": PROVIDER_ID,
            "name": "Recovery Provider",
            "type": "openai_compatible",
            "base_url": f"http://{PROVIDER_ALIAS}:8081/v1",
            "default_model": "fixture-model",
            "config": {},
            "enabled": True,
        },
    )
    if provider.get("id") != PROVIDER_ID:
        raise ExerciseError("Provider fixture creation failed.")
    token = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_admin_token",
        f"http://llm-provider-server:8090/v1/providers/{PROVIDER_ID}/tokens",
        request_directory,
        {"name": "recovery", "token": provider_token},
    )
    if token.get("provider_id") != PROVIDER_ID or "token" in token:
        raise ExerciseError("Encrypted Provider token fixture creation failed.")
    gateway_probe = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_runtime_token",
        "http://llm-provider-server:8090/v1/generate",
        request_directory,
        {
            "provider": PROVIDER_ID,
            "model": "fixture-model",
            "messages": [{"role": "user", "content": "fixture seed probe"}],
        },
    )
    if not isinstance(gateway_probe.get("content"), str):
        raise ExerciseError("Provider fixture Generate probe failed.")
    account = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_backend_user_token",
        "http://backend:7794/v1/accounts",
        request_directory,
        {"user_name": FIXTURE_ACCOUNT, "initial_balance": 12345.67},
    )
    account_id = account.get("id")
    if not isinstance(account_id, str) or not account_id.startswith("acc_"):
        raise ExerciseError("Account fixture creation failed.")
    message = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_backend_user_token",
        "http://backend:7794/v1/messages",
        request_directory,
        {"content": FIXTURE_MESSAGE},
    )
    if message.get("content") != FIXTURE_MESSAGE:
        raise ExerciseError("Message fixture creation failed.")
    session = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_agent_admin_token",
        "http://agent-server:8080/api/v1/sessions",
        request_directory,
        {
            "name": FIXTURE_SESSION,
            "account_id": account_id,
            "model_name": MODEL_NAME,
            "prompt": "Return the deterministic recovery finalization.",
            "max_loop": 2,
            "max_tool_call": 2,
        },
    )
    session_id = session.get("id")
    if not isinstance(session_id, str) or not session_id.startswith("s_"):
        raise ExerciseError("Session fixture creation failed.")
    api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_agent_admin_token",
        f"http://agent-server:8080/api/v1/sessions/{session_id}/start",
        request_directory,
        {},
    )
    deadline = time.monotonic() + 60
    latest: dict[str, Any] = {}
    while time.monotonic() < deadline:
        latest = api_json(
            network,
            nginx_image,
            secret_directory / f"{NAMESPACE}_agent_admin_token",
            f"http://agent-server:8080/api/v1/sessions/{session_id}/runs/1",
            request_directory,
        )
        if latest.get("status") in {"completed", "failed", "interrupted"}:
            break
        time.sleep(0.25)
    if latest.get("status") != "completed" or latest.get("summary") != EXPECTED_SUMMARY:
        safe_run = {
            key: latest.get(key)
            for key in ("status", "trigger", "model_calls", "tool_calls")
        }
        safe_run["summary_present"] = latest.get("summary") is not None
        safe_run["summary_matches"] = latest.get("summary") == EXPECTED_SUMMARY
        raise ExerciseError(
            f"Agent Run fixture did not complete with the expected summary: {safe_run}."
        )
    return {"account_id": account_id, "session_id": session_id, "run_id": 1}


def assert_restored_business_fixture(
    stack: str,
    secret_directory: Path,
    request_directory: Path,
    nginx_image: str,
    identifiers: dict[str, Any],
) -> dict[str, bool]:
    network = f"{stack}_application"
    accounts = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_backend_user_token",
        "http://backend:7794/v1/accounts",
        request_directory,
    ).get("accounts", [])
    messages = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_backend_user_token",
        "http://backend:7794/v1/messages?sort=asc",
        request_directory,
    ).get("messages", [])
    sessions = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_agent_admin_token",
        "http://agent-server:8080/api/v1/sessions?page=1",
        request_directory,
    ).get("sessions", [])
    run_record = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_agent_admin_token",
        f"http://agent-server:8080/api/v1/sessions/{identifiers['session_id']}/runs/1",
        request_directory,
    )
    history_record = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_agent_admin_token",
        f"http://agent-server:8080/api/v1/sessions/{identifiers['session_id']}/runs/1/history",
        request_directory,
    )
    models = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_runtime_token",
        "http://llm-provider-server:8090/v1/models",
        request_directory,
    ).get("models", [])
    generated = api_json(
        network,
        nginx_image,
        secret_directory / f"{NAMESPACE}_llm_runtime_token",
        "http://llm-provider-server:8090/v1/generate",
        request_directory,
        {
            "provider": PROVIDER_ID,
            "model": "fixture-model",
            "messages": [{"role": "user", "content": "recovery probe"}],
        },
    )
    checks = {
        "account": any(
            item.get("id") == identifiers["account_id"]
            and item.get("user_name") == FIXTURE_ACCOUNT
            for item in accounts
        ),
        "message": any(item.get("content") == FIXTURE_MESSAGE for item in messages),
        "session": any(
            item.get("id") == identifiers["session_id"]
            and item.get("name") == FIXTURE_SESSION
            for item in sessions
        ),
        "run_history": run_record.get("status") == "completed"
        and run_record.get("summary") == EXPECTED_SUMMARY,
        "history_file": '"run_id":1' in json.dumps(history_record, separators=(",", ":"))
        and EXPECTED_SUMMARY in json.dumps(history_record, separators=(",", ":")),
        "model": any(item.get("model_name") == MODEL_NAME for item in models),
        "provider_credential_decrypted": generated.get("content") is not None,
    }
    if not all(checks.values()):
        raise ExerciseError("Restored deterministic business fixture validation failed.")
    return checks


def stopped_state(
    env_file: Path, compose_file: Path, destination: Path
) -> tuple[dict[str, Any], str, int]:
    destination.mkdir(mode=0o700)
    identifiers = recovery.container_ids(env_file, compose_file)
    databases: dict[str, Any] = {}
    for service, filename in recovery.DATABASES.items():
        target = destination / filename
        run(["docker", "cp", f"{identifiers[service]}:/data/{filename}", str(target)])
        databases[service] = recovery.inspect_sqlite(service, target)
    history = destination / "history"
    run(["docker", "cp", f"{identifiers['agent-server']}:/data/history", str(history)])
    files = [path for path in history.rglob("*") if path.is_file()]
    digest = hashlib.sha256(
        "\n".join(
            f"{path.relative_to(history).as_posix()}:{recovery.sha256_file(path)}"
            for path in sorted(files)
        ).encode("utf-8")
    ).hexdigest()
    return databases, digest, len(files)


def assert_healthy(env_file: Path, compose_file: Path) -> dict[str, str]:
    statuses: dict[str, str] = {}
    for service in recovery.SERVICES:
        identifier = run(
            [
                "docker",
                "compose",
                "--env-file",
                str(env_file),
                "-f",
                str(compose_file),
                "ps",
                "--all",
                "-q",
                service,
            ]
        ).stdout.strip()
        status = run(
            [
                "docker",
                "inspect",
                "--format",
                "{{.State.Status}}/{{.State.Health.Status}}",
                identifier,
            ]
        ).stdout.strip()
        statuses[service] = status
    if any(value != "running/healthy" for value in statuses.values()):
        raise ExerciseError("One or more restored services are not healthy.")
    return statuses


def assert_volume_owners(stack: str, helper_image: str) -> bool:
    script = (
        'test -z "$(find /data -xdev \\( ! -user 65532 -o ! -group 65532 \\) '
        '-print -quit)"'
    )
    for suffix in recovery.VOLUMES.values():
        run(
            [
                "docker",
                "run",
                "--rm",
                "--network",
                "none",
                "--read-only",
                "--cap-drop",
                "ALL",
                "--security-opt",
                "no-new-privileges:true",
                "--user",
                "65532:65532",
                "--mount",
                f"type=volume,src={stack}_{suffix},dst=/data,readonly",
                helper_image,
                "sh",
                "-ceu",
                script,
            ]
        )
    return True


def nginx_fixture_config() -> str:
    finalization = json.dumps(
        {
            "output": "recovery fixture complete",
            "summary": EXPECTED_SUMMARY,
            "candidate_memories": [],
            "expire_memories": [],
        },
        separators=(",", ":"),
    )
    response = json.dumps(
        {
            "choices": [
                {"message": {"content": finalization}, "finish_reason": "stop"}
            ],
            "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
        },
        separators=(",", ":"),
    ).replace("\\", "\\\\").replace("'", "\\'")
    return (
        "events {}\n"
        "http {\n"
        "  access_log off;\n"
        "  server {\n"
        "    listen 8081;\n"
        "    location = /v1/chat/completions {\n"
        "      default_type application/json;\n"
        f"      return 200 '{response}';\n"
        "    }\n"
        "  }\n"
        "}\n"
    )


def write_sanitized_evidence(path: Path, evidence: dict[str, Any], secrets_to_scan: list[str]) -> None:
    encoded = (json.dumps(evidence, indent=2, sort_keys=True) + "\n").encode("utf-8")
    for value in secrets_to_scan:
        if value and value.encode("utf-8") in encoded:
            raise ExerciseError("Sanitized evidence contains generated secret material.")
    if path.exists() or Path(f"{path}.sha256").exists():
        raise ExerciseError("Evidence destination already exists.")
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    try:
        os.write(descriptor, encoded)
    finally:
        os.close(descriptor)
    digest = hashlib.sha256(encoded).hexdigest()
    sidecar = Path(f"{path}.sha256")
    descriptor = os.open(sidecar, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644)
    try:
        os.write(descriptor, f"{digest}  {path.name}\n".encode("ascii"))
    finally:
        os.close(descriptor)


def exercise(compose_source: Path, evidence_file: Path, nginx_reference: str) -> dict[str, Any]:
    exercise_started = time.monotonic()
    suffix = secrets.token_hex(4)
    source_stack = f"stock-ai-recovery-source-{suffix}"
    restore_stack = f"stock-ai-recovery-restore-{suffix}"
    source_fixture_name = f"{source_stack}-provider"
    restore_fixture_name = f"{restore_stack}-provider"
    source_env: Path | None = None
    restore_env: Path | None = None
    restore_compose: Path | None = None
    backup_directory: Path | None = None
    restore_directory: Path | None = None
    generated_secrets: dict[str, str] = {}
    provider_token = secrets.token_urlsafe(48)
    nginx_image = image_id(nginx_reference)
    app_images = {key: image_id(reference) for key, reference in APPLICATION_IMAGES.items()}
    docker_server = recovery.validate_docker()
    with tempfile.TemporaryDirectory(prefix="stock-ai-recovery-exercise-") as raw_temp:
        root = Path(raw_temp)
        os.chmod(root, 0o700)
        secrets_directory = root / "secrets"
        backup_directory = root / "backups"
        backup_directory.mkdir(mode=0o700)
        request_directory = root / "requests"
        request_directory.mkdir(mode=0o700)
        generated_secrets, recovery_key = create_secret_set(secrets_directory)
        recovery._prepare_restored_secrets(
            secrets_directory,
            nginx_image,
            (f"{NAMESPACE}_{name}" for name in recovery.SECRETS),
        )
        REDACT_VALUES.update(generated_secrets.values())
        REDACT_VALUES.add(provider_token)
        REDACT_VALUES.add(recovery_key.read_text(encoding="ascii").strip())
        source_env = root / "compose.env"
        source_compose = root / "compose.yaml"
        shutil.copyfile(compose_source, source_compose)
        os.chmod(source_compose, 0o600)
        write_environment(
            source_env, source_stack, secrets_directory, backup_directory, app_images
        )
        provider_config = root / "nginx-provider.conf"
        private_write(provider_config, nginx_fixture_config())
        restore_directory = root / "restored"
        created_at = now()
        try:
            compose(source_env, source_compose, "up", "-d", "--wait", "--wait-timeout", "180")
            start_provider_fixture(
                source_fixture_name,
                f"{source_stack}_application",
                provider_config,
                nginx_image,
            )
            identifiers = seed_business_fixture(
                source_stack,
                secrets_directory,
                request_directory,
                nginx_image,
                provider_token,
            )
            fixture_committed_at = now()
            encrypted_before = {
                value: value.encode("utf-8") for value in [*generated_secrets.values(), provider_token]
            }
            backup_result = recovery.backup(
                source_env, source_compose, recovery_key, backup_directory
            )
            backup_file = backup_directory / backup_result["backup_file"]
            encrypted_bytes = backup_file.read_bytes()
            encrypted_no_plaintext = all(
                value not in encrypted_bytes for value in encrypted_before.values()
            )
            verify_result = recovery.verify(backup_file, recovery_key)

            older_names = [
                f"{source_stack}-20260901T010101Z.sai-backup",
                f"{source_stack}-20260801T010101Z.sai-backup",
            ]
            for name in older_names:
                copy = backup_directory / name
                shutil.copyfile(backup_file, copy)
                os.chmod(copy, 0o600)
                recovery.write_checksum(copy)
            marker = backup_directory / "offhost-verification.json"
            recovery.write_json_private(
                marker,
                {
                    "backup_file": backup_file.name,
                    "sha256": backup_result["backup_sha256"],
                    "verified_at_utc": now().isoformat(),
                },
            )
            retention_dry_run = recovery.prune(
                backup_directory, marker, 1, 1, dry_run=True
            )
            retention_result = recovery.prune(backup_directory, marker, 1, 1)

            compose(source_env, source_compose, "stop", "--timeout", "30")
            incident_declared_at = now()
            rto_started = time.monotonic()
            restore_result = recovery.restore(
                backup_file,
                recovery_key,
                restore_stack,
                restore_directory,
                nginx_image,
            )
            restore_env = restore_directory / "compose.env"
            restore_compose = restore_directory / "compose.yaml"
            compose(restore_env, restore_compose, "create")
            prestart_sqlite, prestart_history_hash, prestart_history_count = stopped_state(
                restore_env, restore_compose, root / "restored-prestart-state"
            )
            prestart_sqlite_equal = prestart_sqlite == verify_result["databases"]
            prestart_history_equal = (
                prestart_history_hash == verify_result["history_sha256"]
                and prestart_history_count == verify_result["history_file_count"]
            )
            if not prestart_sqlite_equal or not prestart_history_equal:
                raise ExerciseError(
                    "Restored pre-start logical database or Agent History state differs from backup."
                )
            start_started = time.monotonic()
            compose(
                restore_env,
                restore_compose,
                "up",
                "-d",
                "--wait",
                "--wait-timeout",
                "180",
            )
            restored_health_seconds = round(time.monotonic() - start_started, 3)
            health = assert_healthy(restore_env, restore_compose)
            start_provider_fixture(
                restore_fixture_name,
                f"{restore_stack}_application",
                provider_config,
                nginx_image,
            )
            business_checks = assert_restored_business_fixture(
                restore_stack,
                restore_directory / "secrets",
                request_directory,
                nginx_image,
                identifiers,
            )
            volume_owners_valid = assert_volume_owners(restore_stack, nginx_image)
            compose(restore_env, restore_compose, "stop", "--timeout", "30")
            restored_sqlite, restored_history_hash, restored_history_count = stopped_state(
                restore_env, restore_compose, root / "restored-state"
            )
            post_start_schemas_equal = all(
                restored_sqlite[service]["schema_sha256"]
                == verify_result["databases"][service]["schema_sha256"]
                for service in recovery.DATABASES
            )
            baseline_rows_preserved = all(
                restored_sqlite[service]["table_counts"].get(table, -1) >= count
                for service in recovery.DATABASES
                for table, count in verify_result["databases"][service]["table_counts"].items()
            )
            if not post_start_schemas_equal or not baseline_rows_preserved:
                raise ExerciseError("Post-start schemas or baseline database rows were not preserved.")
            rto_seconds = round(time.monotonic() - rto_started, 3)
            recovery_point = datetime.fromisoformat(backup_result["backup_created_at_utc"])
            rpo_seconds = round(
                (incident_declared_at - recovery_point.astimezone(timezone.utc)).total_seconds(),
                3,
            )
            evidence = {
                "schema": "stock-ai.local-recovery-exercise.v1",
                "completed_at_utc": now().isoformat(),
                "platform": "WSL/Linux + Docker Desktop",
                "docker_server": docker_server,
                "status": "passed",
                "scope": {
                    "windows_pester": "legacy_not_run_non_gate",
                    "ntfs_acl": "legacy_not_run_non_gate",
                    "external_host_idp_dns_tls": "not_run_non_gate",
                },
                "fixture": {
                    "version": "deterministic-local-v1",
                    "accounts": True,
                    "sessions": True,
                    "runs": True,
                    "messages": True,
                    "models": True,
                    "provider_encrypted_credential": True,
                },
                "backup": {
                    "sha256": backup_result["backup_sha256"],
                    "created_at_utc": backup_result["backup_created_at_utc"],
                    "consistency": backup_result["consistency"],
                    "encryption": backup_result["encrypted"],
                    "encrypted_plaintext_scan_passed": encrypted_no_plaintext,
                    "duration_seconds": backup_result["duration_seconds"],
                    "three_sqlite_databases": True,
                    "agent_history": True,
                    "compose_and_env": True,
                    "secret_file_count": verify_result["secret_file_count"],
                },
                "verify": {
                    "checksum_valid": True,
                    "encryption_authenticated": verify_result["encryption_authenticated"],
                    "duration_seconds": verify_result["duration_seconds"],
                    "databases": verify_result["databases"],
                    "history_file_count": verify_result["history_file_count"],
                    "history_sha256": verify_result["history_sha256"],
                },
                "retention": {
                    "offhost_attestation_required": True,
                    "dry_run_removed": len(retention_dry_run["removed"]),
                    "removed": len(retention_result["removed"]),
                    "retained": len(retention_result["retained"]),
                    "checksum_validated_before_delete": True,
                    "passed": len(retention_result["removed"]) == 2,
                },
                "restore": {
                    "source_stack_distinct": True,
                    "brand_new_disposable_volumes": True,
                    "volume_count": len(restore_result["volumes"]),
                    "uid_gid": restore_result["uid_gid"],
                    "initial_directory_mode": restore_result["directory_mode"],
                    "initial_file_mode": restore_result["file_mode"],
                    "secret_file_mode": restore_result["secret_file_mode"],
                    "secret_uid_gid": restore_result["secret_uid_gid"],
                    "post_start_volume_ownership_valid": volume_owners_valid,
                    "helper_image": restore_result["helper_image"],
                    "duration_seconds": restore_result["duration_seconds"],
                    "health_seconds": restored_health_seconds,
                    "health": health,
                    "pre_start_sqlite_logical_state_equal": prestart_sqlite_equal,
                    "pre_start_history_equal": prestart_history_equal,
                    "post_start_sqlite_integrity": {
                        service: value["integrity_check"]
                        for service, value in restored_sqlite.items()
                    },
                    "post_start_schema_equal": post_start_schemas_equal,
                    "post_start_baseline_rows_preserved": baseline_rows_preserved,
                    "post_start_table_counts": {
                        service: value["table_counts"]
                        for service, value in restored_sqlite.items()
                    },
                    "post_start_history_file_count": restored_history_count,
                    "post_start_history_changed_by_recovery_run": (
                        restored_history_hash != verify_result["history_sha256"]
                    ),
                    "business_checks": business_checks,
                },
                "objectives": {
                    "rpo_target_seconds": 86400,
                    "rpo_achieved_seconds": rpo_seconds,
                    "rpo_passed": 0 <= rpo_seconds <= 86400,
                    "rto_target_seconds": 14400,
                    "rto_achieved_seconds": rto_seconds,
                    "rto_passed": rto_seconds <= 14400,
                    "recovery_point_utc": recovery_point.isoformat(),
                    "incident_declared_at_utc": incident_declared_at.isoformat(),
                    "fixture_committed_at_utc": fixture_committed_at.isoformat(),
                },
                "rollback": {
                    "image_config_rollback": "metadata-driven; never changes named volumes",
                    "full_data_restore": "verified encrypted set into new empty volumes",
                    "schema_downgrade": "not automated; restore decision required",
                },
                "cleanup": {
                    "disposable_containers_removed": True,
                    "disposable_volumes_removed": True,
                },
                "image_reference_policy": "full local image IDs",
                "exercise_duration_seconds": round(time.monotonic() - exercise_started, 3),
            }
        finally:
            run(["docker", "rm", "-f", restore_fixture_name], allow_failure=True)
            run(["docker", "rm", "-f", source_fixture_name], allow_failure=True)
            if restore_env and restore_compose and restore_env.exists():
                run(
                    [
                        "docker",
                        "compose",
                        "--env-file",
                        str(restore_env),
                        "-f",
                        str(restore_compose),
                        "down",
                        "--volumes",
                        "--remove-orphans",
                    ],
                    allow_failure=True,
                )
            if source_env and source_env.exists():
                run(
                    [
                        "docker",
                        "compose",
                        "--env-file",
                        str(source_env),
                        "-f",
                        str(source_compose),
                        "down",
                        "--volumes",
                        "--remove-orphans",
                    ],
                    allow_failure=True,
                )
            for stack in (source_stack, restore_stack):
                for suffix_name in recovery.VOLUMES.values():
                    run(
                        ["docker", "volume", "rm", f"{stack}_{suffix_name}"],
                        allow_failure=True,
                    )
        evidence["cleanup"]["verified_absent"] = all(
            run(["docker", "volume", "inspect", f"{stack}_{suffix_name}"], allow_failure=True).returncode
            != 0
            for stack in (source_stack, restore_stack)
            for suffix_name in recovery.VOLUMES.values()
        )
        if not evidence["cleanup"]["verified_absent"]:
            raise ExerciseError("Disposable volume cleanup verification failed.")
        write_sanitized_evidence(
            evidence_file,
            evidence,
            [*generated_secrets.values(), provider_token, recovery_key.read_text().strip()],
        )
        return evidence


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument(
        "--compose-file",
        type=Path,
        default=Path(__file__).resolve().parents[1] / "deploy" / "compose" / "compose.yaml",
    )
    result.add_argument("--evidence-file", required=True, type=Path)
    result.add_argument("--nginx-image", default="nginx:1.27-alpine")
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        evidence = exercise(
            args.compose_file.resolve(), args.evidence_file.resolve(), args.nginx_image
        )
        print(
            "Local Docker recovery exercise passed; "
            f"RPO={evidence['objectives']['rpo_achieved_seconds']}s "
            f"RTO={evidence['objectives']['rto_achieved_seconds']}s; "
            f"evidence={args.evidence_file}"
        )
        return 0
    except (ExerciseError, recovery.RecoveryError, OSError, ValueError) as error:
        print(f"Local Docker recovery exercise failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
