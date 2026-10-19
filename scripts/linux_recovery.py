#!/usr/bin/env python3
"""Encrypted, stop-the-world recovery sets for Linux/WSL Docker Compose.

The command deliberately reports metadata only.  It never prints environment or
secret values, decrypted archive paths, or subprocess output that could contain
them.
"""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import os
import re
import shlex
import shutil
import sqlite3
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable


try:
    from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
except ImportError:  # pragma: no cover - exercised by the operator preflight
    Cipher = algorithms = modes = None  # type: ignore[assignment]


SCHEMA = 2
MAGIC = b"SAI-RECOVERY\x00\x02"
NONCE_BYTES = 12
TAG_BYTES = 16
CHUNK_BYTES = 1024 * 1024
HELPER_IMAGE = (
    "golang:1.27.2-alpine3.23@sha256:"
    "9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079"
)
SERVICES = ("backend", "llm-provider-server", "agent-server", "middleware")
VOLUMES = {
    "backend": "backend_state",
    "llm-provider-server": "llm_state",
    "agent-server": "agent_state",
}
DATABASES = {
    "backend": "backend.db",
    "llm-provider-server": "llm-provider.db",
    "agent-server": "agent-server.db",
}
SECRETS = (
    "backend_user_token",
    "backend_middleware_token",
    "backend_account_token_key",
    "agent_admin_token",
    "llm_admin_token",
    "llm_runtime_token",
    "llm_master_key",
    "oidc_client_secret",
    "oidc_cookie_key",
)
BACKUP_NAME = re.compile(
    r"^(?P<stack>[a-z0-9][a-z0-9-]{1,62})-(?P<stamp>\d{8}T\d{6}Z)\.sai-backup$"
)
RESTORE_STACK = re.compile(
    r"^[a-z0-9][a-z0-9-]{1,47}-restore-[a-z0-9]{4,16}$"
)
IMMUTABLE_IMAGE = re.compile(
    r"(?:sha256:[0-9a-f]{64}|[^\s@]+@sha256:[0-9a-f]{64})"
)


class RecoveryError(RuntimeError):
    """Expected policy or workflow failure with a secret-free message."""


def utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _write_private(path: Path, content: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        os.write(descriptor, content)
    finally:
        os.close(descriptor)


def write_json_private(path: Path, value: object) -> None:
    _write_private(
        path,
        (json.dumps(value, indent=2, sort_keys=True) + "\n").encode("utf-8"),
    )


def ensure_private_directory(path: Path, *, create: bool = True) -> Path:
    if path.is_symlink():
        raise RecoveryError("A recovery directory must not be a symbolic link.")
    path = path.resolve(strict=False)
    if create:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not path.is_dir():
        raise RecoveryError("A recovery directory is missing or is not a directory.")
    os.chmod(path, 0o700)
    if stat.S_IMODE(path.stat().st_mode) != 0o700:
        raise RecoveryError("A recovery directory is not mode 0700.")
    return path


def require_private_file(path: Path, label: str) -> Path:
    if path.is_symlink() or not path.is_file():
        raise RecoveryError(f"{label} must be a regular file and not a symbolic link.")
    if stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise RecoveryError(f"{label} must not grant group or other permissions.")
    return path


def require_linux_secret_file(path: Path, label: str) -> Path:
    if path.is_symlink() or not path.is_file():
        raise RecoveryError(f"{label} must be a regular file and not a symbolic link.")
    info = path.stat()
    mode = stat.S_IMODE(info.st_mode)
    if info.st_uid != 65532 or info.st_gid != os.getgid() or mode != 0o440:
        raise RecoveryError(
            f"{label} must be uid 65532, operator gid {os.getgid()}, and mode 0440."
        )
    return path


def parse_env(path: Path) -> tuple[list[str], dict[str, str]]:
    require_private_file(path, "Compose environment file")
    lines = path.read_text(encoding="utf-8").splitlines()
    values: dict[str, str] = {}
    for raw in lines:
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        key = key.strip()
        if not separator or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", key):
            raise RecoveryError("Compose environment file has an invalid assignment.")
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        values[key] = value
    return lines, values


def recovery_key(path: Path) -> bytes:
    require_private_file(path, "Recovery key file")
    try:
        encoded = path.read_text(encoding="ascii").strip()
        decoded = base64.b64decode(encoded, validate=True)
    except (OSError, UnicodeError, ValueError, binascii.Error) as error:
        raise RecoveryError("Recovery key must be canonical base64.") from error
    if len(decoded) != 32 or base64.b64encode(decoded).decode("ascii") != encoded:
        raise RecoveryError("Recovery key must encode exactly 32 bytes.")
    return decoded


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(CHUNK_BYTES):
            digest.update(chunk)
    return digest.hexdigest()


def checksum_path(backup: Path) -> Path:
    return Path(f"{backup}.sha256")


def write_checksum(backup: Path) -> str:
    digest = sha256_file(backup)
    _write_private(checksum_path(backup), f"{digest}  {backup.name}\n".encode("ascii"))
    return digest


def verify_checksum(backup: Path) -> str:
    require_private_file(backup, "Encrypted backup")
    sidecar = require_private_file(checksum_path(backup), "Backup checksum sidecar")
    expected = sidecar.read_text(encoding="ascii").strip()
    match = re.fullmatch(r"([0-9a-f]{64})  ([^/]+)", expected)
    if not match or match.group(2) != backup.name:
        raise RecoveryError("Backup checksum sidecar is invalid.")
    actual = sha256_file(backup)
    if actual != match.group(1):
        raise RecoveryError("Encrypted backup checksum verification failed.")
    return actual


def encrypt_file(source: Path, destination: Path, key: bytes) -> None:
    if Cipher is None:
        raise RecoveryError("Python cryptography is required for AES-256-GCM backup encryption.")
    nonce = os.urandom(NONCE_BYTES)
    aad = MAGIC + nonce
    encryptor = Cipher(algorithms.AES(key), modes.GCM(nonce)).encryptor()
    encryptor.authenticate_additional_data(aad)
    descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as output, source.open("rb") as plain:
            output.write(aad)
            while chunk := plain.read(CHUNK_BYTES):
                output.write(encryptor.update(chunk))
            output.write(encryptor.finalize())
            output.write(encryptor.tag)
    except Exception:
        destination.unlink(missing_ok=True)
        raise


def decrypt_file(source: Path, destination: Path, key: bytes) -> None:
    if Cipher is None:
        raise RecoveryError("Python cryptography is required for AES-256-GCM backup decryption.")
    size = source.stat().st_size
    header_size = len(MAGIC) + NONCE_BYTES
    if size <= header_size + TAG_BYTES:
        raise RecoveryError("Encrypted backup envelope is truncated.")
    with source.open("rb") as encrypted:
        header = encrypted.read(header_size)
        if header[: len(MAGIC)] != MAGIC:
            raise RecoveryError("Encrypted backup envelope version is unsupported.")
        nonce = header[len(MAGIC) :]
        encrypted.seek(-TAG_BYTES, os.SEEK_END)
        tag = encrypted.read(TAG_BYTES)
        encrypted.seek(header_size)
        remaining = size - header_size - TAG_BYTES
        decryptor = Cipher(algorithms.AES(key), modes.GCM(nonce, tag)).decryptor()
        decryptor.authenticate_additional_data(header)
        descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(descriptor, "wb") as plain:
                while remaining:
                    chunk = encrypted.read(min(CHUNK_BYTES, remaining))
                    if not chunk:
                        raise RecoveryError("Encrypted backup envelope is truncated.")
                    remaining -= len(chunk)
                    plain.write(decryptor.update(chunk))
                plain.write(decryptor.finalize())
        except Exception as error:
            destination.unlink(missing_ok=True)
            if isinstance(error, RecoveryError):
                raise
            raise RecoveryError("Encrypted backup authentication failed.") from error


def inventory(root: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise RecoveryError("Recovery content must not contain symbolic links.")
        if path.is_file() and path.name != "manifest.json":
            result[path.relative_to(root).as_posix()] = sha256_file(path)
    return result


def build_archive(stage: Path, archive: Path) -> None:
    descriptor = os.open(archive, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.close(descriptor)
    try:
        with tarfile.open(archive, "w:gz") as tar:
            for path in sorted(stage.rglob("*")):
                tar.add(path, arcname=path.relative_to(stage), recursive=False)
    except Exception:
        archive.unlink(missing_ok=True)
        raise


def extract_archive(archive: Path, destination: Path) -> None:
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        for member in members:
            candidate = Path(member.name)
            if (
                candidate.is_absolute()
                or ".." in candidate.parts
                or not (member.isdir() or member.isfile())
            ):
                raise RecoveryError("Backup archive contains an unsafe entry.")
        tar.extractall(destination, members=members, filter="data")


def run(args: list[str], *, allow_failure: bool = False) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(args, text=True, capture_output=True, check=False)
    if completed.returncode and not allow_failure:
        command = " ".join(args[:3])
        raise RecoveryError(f"Required command failed with exit {completed.returncode}: {command}.")
    return completed


def compose_args(env_file: Path, compose_file: Path, *args: str) -> list[str]:
    return [
        "docker",
        "compose",
        "--env-file",
        str(env_file),
        "-f",
        str(compose_file),
        *args,
    ]


def validate_docker() -> str:
    completed = run(["docker", "version", "--format", "{{.Server.Os}} {{.Server.Version}}"])
    server = completed.stdout.strip()
    if not server.startswith("linux "):
        raise RecoveryError("Docker Engine must be in Linux-container mode.")
    return server


def container_ids(env_file: Path, compose_file: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for service in VOLUMES:
        completed = run(compose_args(env_file, compose_file, "ps", "--all", "-q", service))
        identifiers = [line for line in completed.stdout.splitlines() if line.strip()]
        if len(identifiers) != 1:
            raise RecoveryError(f"Compose service {service} must have exactly one container.")
        result[service] = identifiers[0]
    return result


def validate_compose(env_file: Path, compose_file: Path) -> dict[str, Any]:
    completed = run(compose_args(env_file, compose_file, "config", "--format", "json"))
    try:
        config = json.loads(completed.stdout)
        services = config["services"]
    except (json.JSONDecodeError, KeyError, TypeError) as error:
        raise RecoveryError("Docker Compose did not return valid configuration metadata.") from error
    if set(services) != set(SERVICES):
        raise RecoveryError("Compose configuration has an unexpected service set.")
    return config


def copy_container_data(container: str, destination: Path) -> None:
    destination.mkdir(mode=0o700)
    run(["docker", "cp", f"{container}:/data/.", str(destination)])


def _copy_private(source: Path, destination: Path) -> None:
    shutil.copyfile(source, destination)
    os.chmod(destination, 0o600)


def required_backup_paths(root: Path) -> list[Path]:
    paths = [root / service / database for service, database in DATABASES.items()]
    paths.extend(
        [
            root / "agent-server" / "history",
            root / "config" / "compose.yaml",
            root / "config" / "compose.env",
            *(root / "secrets" / name for name in SECRETS),
        ]
    )
    return paths


def validate_application_images(value: object) -> dict[str, str]:
    if not isinstance(value, dict) or set(value) != set(SERVICES):
        raise RecoveryError("Backup image metadata is incomplete.")
    images: dict[str, str] = {}
    for service in SERVICES:
        reference = value.get(service)
        if not isinstance(reference, str) or not IMMUTABLE_IMAGE.fullmatch(reference):
            raise RecoveryError(
                "Application images must use a full local SHA-256 ID or repository digest."
            )
        images[service] = reference
    return images


def validate_contents(root: Path) -> dict[str, Any]:
    manifest_path = root / "manifest.json"
    if manifest_path.is_symlink() or not manifest_path.is_file():
        raise RecoveryError("Backup manifest is missing.")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise RecoveryError("Backup manifest is invalid.") from error
    if manifest.get("schema") != SCHEMA or not isinstance(manifest.get("files"), dict):
        raise RecoveryError("Backup manifest schema is unsupported.")
    if manifest["files"] != inventory(root):
        raise RecoveryError("Backup internal checksum inventory does not match.")
    for path in required_backup_paths(root):
        if path.name == "history":
            if path.is_symlink() or not path.is_dir():
                raise RecoveryError("Backup is missing Agent History.")
        elif path.is_symlink() or not path.is_file() or path.stat().st_size == 0:
            raise RecoveryError("Backup is missing required recovery content.")
    manifest["images"] = validate_application_images(manifest.get("images"))
    return manifest


def inspect_sqlite(label: str, path: Path) -> dict[str, Any]:
    connection = sqlite3.connect(f"file:{path.as_posix()}?mode=ro", uri=True)
    try:
        result = connection.execute("PRAGMA integrity_check").fetchone()
        if not result or result[0] != "ok":
            raise RecoveryError(f"{label} SQLite integrity_check failed.")
        tables = [
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master "
                "WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
            )
        ]
        counts = {
            table: connection.execute(
                f'SELECT COUNT(*) FROM "{table.replace(chr(34), chr(34) * 2)}"'
            ).fetchone()[0]
            for table in tables
        }
        schema = "\n".join(
            row[0] or ""
            for row in connection.execute(
                "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name"
            )
        )
    except sqlite3.Error as error:
        raise RecoveryError(f"{label} SQLite validation failed.") from error
    finally:
        connection.close()
    return {
        "integrity_check": "ok",
        "schema_sha256": hashlib.sha256(schema.encode("utf-8")).hexdigest(),
        "table_counts": counts,
    }


def sanitized_validation(root: Path, manifest: dict[str, Any]) -> dict[str, Any]:
    databases = {
        service: inspect_sqlite(service, root / service / filename)
        for service, filename in DATABASES.items()
    }
    history = root / "agent-server" / "history"
    history_files = [path for path in history.rglob("*") if path.is_file()]
    return {
        "archive_schema": manifest["schema"],
        "backup_created_at_utc": manifest["created_at_utc"],
        "environment": manifest["environment"],
        "source_stack": manifest["stack"],
        "databases": databases,
        "history_file_count": len(history_files),
        "history_sha256": hashlib.sha256(
            "\n".join(
                f"{path.relative_to(history).as_posix()}:{sha256_file(path)}"
                for path in sorted(history_files)
            ).encode("utf-8")
        ).hexdigest(),
        "secret_file_count": len(SECRETS),
        "images_recorded": sorted(manifest["images"]),
    }


def unpack_verified(backup: Path, key_file: Path, temporary: Path) -> tuple[Path, dict[str, Any], str]:
    digest = verify_checksum(backup)
    archive = temporary / "recovery.tar.gz"
    decrypt_file(backup, archive, recovery_key(key_file))
    contents = temporary / "contents"
    contents.mkdir(mode=0o700)
    extract_archive(archive, contents)
    archive.unlink()
    return contents, validate_contents(contents), digest


def backup(
    env_file: Path,
    compose_file: Path,
    key_file: Path,
    backup_directory: Path | None = None,
) -> dict[str, Any]:
    started_clock = time.monotonic()
    started = utc_now()
    _, values = parse_env(env_file)
    required = (
        "STOCK_AI_INSTANCE",
        "STOCK_AI_ENVIRONMENT",
        "STOCK_AI_SECRET_NAMESPACE",
        "STOCK_AI_SECRETS_DIR",
    )
    if any(not values.get(name) for name in required):
        raise RecoveryError("Compose environment is missing required recovery metadata.")
    stack = values["STOCK_AI_INSTANCE"]
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{1,62}", stack):
        raise RecoveryError("Compose stack name is invalid.")
    directory_value = backup_directory or Path(values.get("STOCK_AI_BACKUP_DIR", ""))
    if not str(directory_value):
        raise RecoveryError("Set STOCK_AI_BACKUP_DIR or --backup-directory.")
    backup_directory = ensure_private_directory(Path(directory_value))
    secret_directory = ensure_private_directory(Path(values["STOCK_AI_SECRETS_DIR"]), create=False)
    namespace = values["STOCK_AI_SECRET_NAMESPACE"]
    secret_paths: dict[str, Path] = {}
    for name in SECRETS:
        secret_paths[name] = require_linux_secret_file(
            secret_directory / f"{namespace}_{name}", f"Required secret {name}"
        )
    recovery_key(key_file)
    validate_docker()
    config = validate_compose(env_file, compose_file)
    images = validate_application_images(
        {service: config["services"][service].get("image") for service in SERVICES}
    )
    running = bool(
        run(compose_args(env_file, compose_file, "ps", "--status", "running", "-q"))
        .stdout.strip()
    )
    stamp = started.strftime("%Y%m%dT%H%M%SZ")
    destination = backup_directory / f"{stack}-{stamp}.sai-backup"
    if destination.exists() or checksum_path(destination).exists():
        raise RecoveryError("Timestamped backup destination already exists.")
    with tempfile.TemporaryDirectory(prefix="stock-ai-backup-", dir=backup_directory) as raw_temp:
        temp = Path(raw_temp)
        os.chmod(temp, 0o700)
        stage = temp / "contents"
        stage.mkdir(mode=0o700)
        archive = temp / "recovery.tar.gz"
        try:
            run(compose_args(env_file, compose_file, "stop", "--timeout", "30"))
            containers = container_ids(env_file, compose_file)
            for service, identifier in containers.items():
                copy_container_data(identifier, stage / service)
            config_directory = stage / "config"
            secrets_directory = stage / "secrets"
            config_directory.mkdir(mode=0o700)
            secrets_directory.mkdir(mode=0o700)
            _copy_private(compose_file, config_directory / "compose.yaml")
            _copy_private(env_file, config_directory / "compose.env")
            for name, source in secret_paths.items():
                _copy_private(source, secrets_directory / name)
            manifest = {
                "schema": SCHEMA,
                "created_at_utc": started.isoformat(),
                "environment": values["STOCK_AI_ENVIRONMENT"],
                "stack": stack,
                "consistency": "compose_stopped",
                "uid_gid": "65532:65532",
                "volumes": list(VOLUMES.values()),
                "images": images,
                "files": inventory(stage),
            }
            write_json_private(stage / "manifest.json", manifest)
            validate_contents(stage)
            build_archive(stage, archive)
            encrypt_file(archive, destination, recovery_key(key_file))
            digest = write_checksum(destination)
        finally:
            if running:
                run(
                    compose_args(
                        env_file,
                        compose_file,
                        "up",
                        "-d",
                        "--wait",
                        "--wait-timeout",
                        "180",
                    )
                )
    return {
        "action": "backup",
        "backup_file": destination.name,
        "backup_sha256": digest,
        "backup_created_at_utc": started.isoformat(),
        "consistency": "compose_stopped",
        "encrypted": "AES-256-GCM",
        "duration_seconds": round(time.monotonic() - started_clock, 3),
    }


def verify(backup_file: Path, key_file: Path) -> dict[str, Any]:
    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="stock-ai-verify-") as raw_temp:
        temp = Path(raw_temp)
        os.chmod(temp, 0o700)
        contents, manifest, digest = unpack_verified(backup_file, key_file, temp)
        result = sanitized_validation(contents, manifest)
    result.update(
        {
            "action": "verify",
            "backup_file": backup_file.name,
            "backup_sha256": digest,
            "encryption_authenticated": True,
            "duration_seconds": round(time.monotonic() - started, 3),
        }
    )
    return result


def _volume_exists(name: str) -> bool:
    return run(["docker", "volume", "inspect", name], allow_failure=True).returncode == 0


def _create_volume(name: str, source_digest: str) -> None:
    run(
        [
            "docker",
            "volume",
            "create",
            "--label",
            "stock-ai.recovery.disposable=true",
            "--label",
            f"stock-ai.recovery.source-sha256={source_digest}",
            name,
        ]
    )


def _remove_volumes(names: Iterable[str]) -> None:
    for name in names:
        run(["docker", "volume", "rm", name], allow_failure=True)


def _immutable_helper(reference: str) -> str:
    if not IMMUTABLE_IMAGE.fullmatch(reference):
        raise RecoveryError("Recovery helper image must be pinned by a full SHA-256 digest.")
    return reference


def _restore_volume(source: Path, volume: str, helper_image: str) -> None:
    copy_script = (
        'test -z "$(find /data -mindepth 1 -print -quit)"; '
        "cp -a /source/. /data/; "
        'test -z "$(find /data -xdev -type l -print -quit)"; '
        "chown -R 65532:65532 /data; "
        "find /data -xdev -type d -exec chmod 0700 {} +; "
        "find /data -xdev -type f -exec chmod 0600 {} +"
    )
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
            "--cap-add",
            "CHOWN",
            "--cap-add",
            "FOWNER",
            "--cap-add",
            "DAC_OVERRIDE",
            "--security-opt",
            "no-new-privileges:true",
            "--pids-limit",
            "64",
            "--mount",
            f"type=bind,src={source},dst=/source,readonly",
            "--mount",
            f"type=volume,src={volume},dst=/data",
            helper_image,
            "sh",
            "-ceu",
            copy_script,
        ]
    )
    verify_script = (
        'test -z "$(find /data -xdev \\( ! -user 65532 -o ! -group 65532 \\) -print -quit)"; '
        'test -z "$(find /data -xdev -type d ! -perm 0700 -print -quit)"; '
        'test -z "$(find /data -xdev -type f ! -perm 0600 -print -quit)"'
    )
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
            "--pids-limit",
            "64",
            "--user",
            "65532:65532",
            "--mount",
            f"type=volume,src={volume},dst=/data,readonly",
            helper_image,
            "sh",
            "-ceu",
            verify_script,
        ]
    )


def _prepare_restored_secrets(
    directory: Path, helper_image: str, filenames: Iterable[str]
) -> None:
    operator_gid = os.getgid()
    names = tuple(filenames)
    if len(names) != len(SECRETS) or any(
        not re.fullmatch(r"[a-z0-9][a-z0-9_-]{1,127}", name) for name in names
    ):
        raise RecoveryError("Exactly six canonical secret filenames are required.")
    targets = " ".join(shlex.quote(f"/secrets/{name}") for name in names)
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
            "--cap-add",
            "CHOWN",
            "--cap-add",
            "FOWNER",
            "--cap-add",
            "DAC_OVERRIDE",
            "--security-opt",
            "no-new-privileges:true",
            "--pids-limit",
            "32",
            "--mount",
            f"type=bind,src={directory},dst=/secrets",
            helper_image,
            "sh",
            "-ceu",
            (
                f"for item in {targets}; do test -f \"$item\" && test ! -L \"$item\"; done; "
                f"chown 65532:{operator_gid} {targets}; chmod 0440 {targets}"
            ),
        ]
    )
    for name in names:
        require_linux_secret_file(directory / name, "Prepared secret")


def prepare_secrets(env_file: Path, helper_image: str = HELPER_IMAGE) -> dict[str, Any]:
    helper_image = _immutable_helper(helper_image)
    _, values = parse_env(env_file)
    namespace = values.get("STOCK_AI_SECRET_NAMESPACE", "")
    raw_directory = values.get("STOCK_AI_SECRETS_DIR", "")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{1,62}", namespace) or not raw_directory:
        raise RecoveryError("Compose secret namespace or directory is invalid.")
    directory = ensure_private_directory(Path(raw_directory), create=False)
    names = tuple(f"{namespace}_{name}" for name in SECRETS)
    for name in names:
        path = directory / name
        if path.is_symlink() or not path.is_file():
            raise RecoveryError("A required Compose secret is missing or unsafe.")
        if stat.S_IMODE(path.stat().st_mode) & 0o007:
            raise RecoveryError("A required Compose secret grants other permissions.")
    validate_docker()
    _prepare_restored_secrets(directory, helper_image, names)
    return {
        "action": "prepare-secrets",
        "secret_file_count": len(names),
        "directory_mode": "0700",
        "secret_file_mode": "0440",
        "secret_uid_gid": f"65532:{os.getgid()}",
        "helper_image": helper_image,
    }


def _rewrite_env(lines: list[str], replacements: dict[str, str]) -> str:
    result: list[str] = []
    seen: set[str] = set()
    for line in lines:
        match = re.match(r"^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*)=", line)
        if match and match.group(2) in replacements:
            key = match.group(2)
            result.append(f"{key}={json.dumps(replacements[key])}")
            seen.add(key)
        else:
            result.append(line)
    for key, value in replacements.items():
        if key not in seen:
            result.append(f"{key}={json.dumps(value)}")
    return "\n".join(result) + "\n"


def restore(
    backup_file: Path,
    key_file: Path,
    restore_stack: str,
    restore_directory: Path,
    helper_image: str = HELPER_IMAGE,
) -> dict[str, Any]:
    started = time.monotonic()
    if not RESTORE_STACK.fullmatch(restore_stack):
        raise RecoveryError("Restore stack must use the disposable '*-restore-<suffix>' form.")
    if restore_directory.exists():
        raise RecoveryError("Restore directory must not already exist.")
    helper_image = _immutable_helper(helper_image)
    validate_docker()
    volume_names = {key: f"{restore_stack}_{value}" for key, value in VOLUMES.items()}
    if any(_volume_exists(name) for name in volume_names.values()):
        raise RecoveryError("Restore requires three brand-new disposable named volumes.")
    created: list[str] = []
    directory_created = False
    try:
        with tempfile.TemporaryDirectory(prefix="stock-ai-restore-") as raw_temp:
            temp = Path(raw_temp)
            os.chmod(temp, 0o700)
            contents, manifest, digest = unpack_verified(backup_file, key_file, temp)
            if restore_stack == manifest["stack"]:
                raise RecoveryError("Restore stack must differ from the source stack.")
            archived_lines, archived_values = parse_env(contents / "config" / "compose.env")
            if archived_values.get("STOCK_AI_INSTANCE") != manifest["stack"]:
                raise RecoveryError("Archived environment and manifest stack do not match.")
            for name in volume_names.values():
                _create_volume(name, digest)
                created.append(name)
            for service, volume in volume_names.items():
                _restore_volume(contents / service, volume, helper_image)
            restore_directory.mkdir(mode=0o700, parents=True)
            directory_created = True
            secrets_directory = restore_directory / "secrets"
            secrets_directory.mkdir(mode=0o700)
            namespace = archived_values.get("STOCK_AI_SECRET_NAMESPACE", "")
            if not namespace:
                raise RecoveryError("Archived secret namespace is missing.")
            _copy_private(contents / "config" / "compose.yaml", restore_directory / "compose.yaml")
            for name in SECRETS:
                _copy_private(
                    contents / "secrets" / name,
                    secrets_directory / f"{namespace}_{name}",
                )
            restored_secret_names = tuple(f"{namespace}_{name}" for name in SECRETS)
            _prepare_restored_secrets(
                secrets_directory, helper_image, restored_secret_names
            )
            env_content = _rewrite_env(
                archived_lines,
                {
                    "STOCK_AI_INSTANCE": restore_stack,
                    "STOCK_AI_SECRETS_DIR": str(secrets_directory.resolve()),
                    "STOCK_AI_BACKUP_DIR": str(backup_file.parent.resolve()),
                },
            )
            _write_private(restore_directory / "compose.env", env_content.encode("utf-8"))
            restored_config = validate_compose(
                restore_directory / "compose.env", restore_directory / "compose.yaml"
            )
            restored_images = validate_application_images(
                {
                    service: restored_config["services"][service].get("image")
                    for service in SERVICES
                }
            )
            if restored_images != manifest["images"]:
                raise RecoveryError("Restored image reference does not match backup metadata.")
            result = {
                "action": "restore",
                "backup_file": backup_file.name,
                "backup_sha256": digest,
                "backup_created_at_utc": manifest["created_at_utc"],
                "source_stack": manifest["stack"],
                "restore_stack": restore_stack,
                "restore_directory": str(restore_directory.resolve()),
                "volumes": sorted(volume_names.values()),
                "volume_policy": "brand_new_disposable",
                "uid_gid": "65532:65532",
                "directory_mode": "0700",
                "file_mode": "0600",
                "secret_file_mode": "0440",
                "secret_uid_gid": f"65532:{os.getgid()}",
                "helper_image": helper_image,
                "duration_seconds": round(time.monotonic() - started, 3),
            }
            write_json_private(restore_directory / "restore-metadata.json", result)
            return result
    except Exception:
        _remove_volumes(created)
        if directory_created:
            shutil.rmtree(restore_directory, ignore_errors=True)
        raise


def _backup_candidates(directory: Path) -> list[tuple[Path, datetime]]:
    candidates: list[tuple[Path, datetime]] = []
    for path in directory.glob("*.sai-backup"):
        if path.is_symlink() or not path.is_file():
            raise RecoveryError("Retention refuses non-regular backup artifacts.")
        match = BACKUP_NAME.fullmatch(path.name)
        if not match:
            raise RecoveryError("Retention found a backup with a noncanonical name.")
        verify_checksum(path)
        timestamp = datetime.strptime(match.group("stamp"), "%Y%m%dT%H%M%SZ").replace(
            tzinfo=timezone.utc
        )
        candidates.append((path, timestamp))
    return sorted(candidates, key=lambda item: (item[1], item[0].name), reverse=True)


def _verified_marker(marker_path: Path, directory: Path, now: datetime) -> Path:
    require_private_file(marker_path, "Off-host verification marker")
    try:
        marker = json.loads(marker_path.read_text(encoding="utf-8"))
        verified = datetime.fromisoformat(str(marker["verified_at_utc"]).replace("Z", "+00:00"))
        digest = str(marker["sha256"])
        name = Path(str(marker["backup_file"])).name
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise RecoveryError("Off-host verification marker is invalid.") from error
    if Path(name).name != str(marker["backup_file"]) or not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise RecoveryError("Off-host verification marker is invalid.")
    if verified.tzinfo is None:
        raise RecoveryError("Off-host verification time must include a timezone.")
    age = now - verified.astimezone(timezone.utc)
    if age < timedelta(minutes=-5) or age > timedelta(days=7):
        raise RecoveryError("Off-host verification marker is stale or from the future.")
    backup = directory / name
    if verify_checksum(backup) != digest:
        raise RecoveryError("Off-host verification marker does not match its backup.")
    return backup


def prune(
    backup_directory: Path,
    marker_path: Path,
    keep_daily: int,
    keep_monthly: int,
    *,
    dry_run: bool = False,
    now: datetime | None = None,
) -> dict[str, Any]:
    if keep_daily < 1 or keep_monthly < 1:
        raise RecoveryError("Retention counts must be positive.")
    directory = ensure_private_directory(backup_directory, create=False)
    now = now or utc_now()
    verified_backup = _verified_marker(marker_path, directory, now)
    candidates = _backup_candidates(directory)
    keep: set[Path] = {verified_backup}
    days: set[str] = set()
    for path, timestamp in candidates:
        day = timestamp.strftime("%Y%m%d")
        if day not in days and len(days) < keep_daily:
            days.add(day)
            keep.add(path)
    months: set[str] = set()
    for path, timestamp in candidates:
        month = timestamp.strftime("%Y%m")
        if month not in months and len(months) < keep_monthly:
            months.add(month)
            keep.add(path)
    removed = [path for path, _ in candidates if path not in keep]
    log_cutoff = now.timestamp() - 90 * 86400
    old_logs = [
        path
        for path in directory.glob("recovery-????????.log")
        if path.is_file() and not path.is_symlink() and path.stat().st_mtime < log_cutoff
    ]
    if not dry_run:
        for path in removed:
            checksum_path(path).unlink()
            path.unlink()
        for path in old_logs:
            path.unlink()
    return {
        "action": "prune",
        "dry_run": dry_run,
        "retained": sorted(path.name for path in keep),
        "removed": sorted(path.name for path in removed),
        "logs_removed": len(old_logs),
        "offhost_verification_required": True,
    }


def _save_evidence(path: Path | None, value: dict[str, Any]) -> None:
    if path is None:
        return
    if path.exists():
        raise RecoveryError("Evidence destination already exists.")
    ensure_private_directory(path.parent)
    write_json_private(path, value)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    actions = result.add_subparsers(dest="action", required=True)

    backup_action = actions.add_parser("backup")
    backup_action.add_argument("--env-file", required=True, type=Path)
    backup_action.add_argument("--compose-file", required=True, type=Path)
    backup_action.add_argument("--key-file", required=True, type=Path)
    backup_action.add_argument("--backup-directory", type=Path)
    backup_action.add_argument("--evidence-file", type=Path)

    verify_action = actions.add_parser("verify")
    verify_action.add_argument("--backup-file", required=True, type=Path)
    verify_action.add_argument("--key-file", required=True, type=Path)
    verify_action.add_argument("--evidence-file", type=Path)

    restore_action = actions.add_parser("restore")
    restore_action.add_argument("--backup-file", required=True, type=Path)
    restore_action.add_argument("--key-file", required=True, type=Path)
    restore_action.add_argument("--restore-stack", required=True)
    restore_action.add_argument("--restore-directory", required=True, type=Path)
    restore_action.add_argument("--helper-image", default=HELPER_IMAGE)
    restore_action.add_argument("--evidence-file", type=Path)

    prune_action = actions.add_parser("prune")
    prune_action.add_argument("--backup-directory", required=True, type=Path)
    prune_action.add_argument("--offhost-marker", required=True, type=Path)
    prune_action.add_argument("--keep-daily", type=int, default=14)
    prune_action.add_argument("--keep-monthly", type=int, default=12)
    prune_action.add_argument("--dry-run", action="store_true")
    prune_action.add_argument("--evidence-file", type=Path)

    prepare_action = actions.add_parser("prepare-secrets")
    prepare_action.add_argument("--env-file", required=True, type=Path)
    prepare_action.add_argument("--helper-image", default=HELPER_IMAGE)
    prepare_action.add_argument("--evidence-file", type=Path)
    return result


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    try:
        if args.action == "backup":
            result = backup(
                args.env_file.resolve(),
                args.compose_file.resolve(),
                args.key_file.resolve(),
                args.backup_directory,
            )
        elif args.action == "verify":
            result = verify(args.backup_file.resolve(), args.key_file.resolve())
        elif args.action == "restore":
            result = restore(
                args.backup_file.resolve(),
                args.key_file.resolve(),
                args.restore_stack,
                args.restore_directory.resolve(),
                args.helper_image,
            )
        elif args.action == "prune":
            result = prune(
                args.backup_directory.resolve(),
                args.offhost_marker.resolve(),
                args.keep_daily,
                args.keep_monthly,
                dry_run=args.dry_run,
            )
        else:
            result = prepare_secrets(args.env_file.resolve(), args.helper_image)
        _save_evidence(args.evidence_file, result)
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    except (RecoveryError, OSError, ValueError) as error:
        print(f"Recovery {args.action} failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
