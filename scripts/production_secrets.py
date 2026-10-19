#!/usr/bin/env python3
"""Generate and validate environment-isolated production secret namespaces.

The commands in this module deliberately never return secret values.  Secret
material is read only for local validation or exact-byte disclosure scans.
"""

from __future__ import annotations

import argparse
import base64
import getpass
import json
import math
import os
import re
import secrets
import stat
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable


SCHEMA_VERSION = 1
ENVIRONMENTS = {"Test": "stock-ai-test", "Release": "stock-ai-release"}
SECRET_SPECS = {
    "backend_user_token": ("token", ("backend", "agent-server")),
    "backend_middleware_token": ("token", ("backend", "middleware")),
    "backend_account_token_key": ("key", ("backend",)),
    "agent_admin_token": ("token", ("agent-server", "middleware")),
    "llm_admin_token": ("token", ("llm-provider-server", "middleware")),
    "llm_runtime_token": ("token", ("llm-provider-server", "agent-server", "middleware")),
    "oidc_client_secret": ("token", ("middleware",)),
    "oidc_cookie_key": ("key", ("middleware",)),
    "llm_master_key": ("key", ("llm-provider-server",)),
}
FORBIDDEN_NAMES = {
    "USER_TOKEN",
    "AGENT_SERVER_ADMIN_TOKEN",
    "LLM_SERVER_ADMIN_TOKEN",
    "LLM_SERVER_RUNTIME_TOKEN",
    "LLM_SERVER_MASTER_KEY",
    "OIDC_CLIENT_SECRET",
    "OIDC_COOKIE_KEY",
    "ACCOUNT_TOKEN_KEY",
}
PLACEHOLDER_PARTS = (
    "change-me",
    "changeme",
    "placeholder",
    "example",
    "development",
    "dev-secret",
    "fixture",
    "password",
    "secret-token",
)


@dataclass(frozen=True)
class Result:
    setting: str
    source: str
    valid: bool
    reason: str

    def diagnostic(self) -> dict[str, object]:
        return {
            "setting": self.setting,
            "source": self.source,
            "valid": self.valid,
            "reason": self.reason,
        }


def namespace_for(environment: str) -> str:
    try:
        return ENVIRONMENTS[environment]
    except KeyError as error:
        raise ValueError("environment must be exactly Test or Release") from error


def _secret_name(namespace: str, setting: str) -> str:
    return f"{namespace}_{setting}"


def _write_exclusive(path: Path, content: bytes, mode: int = 0o600) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    try:
        os.write(descriptor, content)
    finally:
        os.close(descriptor)


def _token() -> str:
    return secrets.token_urlsafe(48)


def _key() -> str:
    return base64.b64encode(secrets.token_bytes(32)).decode("ascii")


def generate(environment: str, directory: Path, backend_database: str, provider_database: str) -> Path:
    namespace = namespace_for(environment)
    if directory.exists():
        raise ValueError("destination already exists; refusing to replace or rotate secrets")
    directory.mkdir(mode=0o700, parents=False)
    generated: list[Path] = []
    try:
        files: dict[str, str] = {}
        mounts: list[dict[str, object]] = []
        for setting, (kind, consumers) in SECRET_SPECS.items():
            filename = _secret_name(namespace, setting)
            path = directory / filename
            value = _key() if kind == "key" else _token()
            _write_exclusive(path, (value + "\n").encode("ascii"))
            generated.append(path)
            files[setting] = filename
            for consumer in consumers:
                mounts.append(
                    {
                        "secret": setting,
                        "service": consumer,
                        "target": f"/run/secrets/{filename}",
                        "read_only": True,
                    }
                )
        manifest = {
            "schema_version": SCHEMA_VERSION,
            "environment": environment,
            "secret_namespace": namespace,
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "legacy_user_auth": False,
            "backend_database": backend_database,
            "provider_database": provider_database,
            "preserve_master_key_with_database": True,
            "secret_files": files,
            "container_mounts": mounts,
        }
        manifest_path = directory / "production-config.json"
        _write_exclusive(
            manifest_path,
            (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode("utf-8"),
        )
        generated.append(manifest_path)
        if os.name == "nt":
            _restrict_windows_acl(directory, generated)
        return manifest_path
    except Exception:
        for path in generated:
            try:
                path.unlink()
            except OSError:
                pass
        try:
            directory.rmdir()
        except OSError:
            pass
        raise


def _restrict_windows_acl(directory: Path, files: Iterable[Path]) -> None:
    principal = getpass.getuser()
    for path in (directory, *files):
        args = [
            "icacls",
            str(path),
            "/inheritance:r",
            "/grant:r",
            f"{principal}:(F)",
            "*S-1-5-18:(F)",
            "*S-1-5-32-544:(F)",
        ]
        completed = subprocess.run(args, capture_output=True, text=True, check=False)
        if completed.returncode != 0:
            raise RuntimeError(f"could not restrict Windows ACL for {path.name}")


def _read_manifest(directory: Path) -> tuple[dict[str, object] | None, Result]:
    path = directory / "production-config.json"
    source = str(path.resolve(strict=False))
    try:
        if path.is_symlink():
            raise ValueError("manifest must not be a symbolic link")
        raw = path.read_text(encoding="utf-8")
        manifest = json.loads(raw)
        if not isinstance(manifest, dict):
            raise ValueError("manifest root must be an object")
        return manifest, Result("production_config", source, True, "valid JSON object")
    except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as error:
        return None, Result("production_config", source, False, type(error).__name__)


def _entropy(value: str) -> float:
    if not value:
        return 0.0
    counts = {character: value.count(character) for character in set(value)}
    return -sum((count / len(value)) * math.log2(count / len(value)) for count in counts.values())


def _has_repeated_block(value: bytes) -> bool:
    return any(
        len(value) % size == 0 and value == value[:size] * (len(value) // size)
        for size in range(1, len(value) // 2 + 1)
    )


def _validate_value(setting: str, value: str, kind: str) -> str | None:
    lowered = value.lower()
    if any(part in lowered for part in PLACEHOLDER_PARTS):
        return "known development or placeholder value"
    if any(value == name or value == name.lower() for name in FORBIDDEN_NAMES):
        return "variable name used as value"
    if kind == "key":
        try:
            decoded = base64.b64decode(value, validate=True)
        except (ValueError, base64.binascii.Error):
            return "must be canonical standard base64"
        if len(decoded) != 32 or base64.b64encode(decoded).decode("ascii") != value:
            return "must encode exactly 32 random bytes"
        if len(set(decoded)) < 16 or _entropy(decoded.decode("latin1")) < 4.2 or _has_repeated_block(decoded):
            return "decoded key has insufficient randomness"
    elif len(value.encode("utf-8")) < 32:
        return "must contain at least 32 bytes"
    if len(set(value)) < 16 or _entropy(value) < 4.0:
        return "insufficient character diversity"
    if re.search(r"(.)\1{5,}", value):
        return "repeated-character pattern"
    return None


def _expected_mounts(namespace: str) -> set[tuple[str, str, str, bool]]:
    expected: set[tuple[str, str, str, bool]] = set()
    for setting, (_, consumers) in SECRET_SPECS.items():
        for consumer in consumers:
            expected.add(
                (setting, consumer, f"/run/secrets/{_secret_name(namespace, setting)}", True)
            )
    return expected


def validate(
    environment: str, directory: Path, other_directory: Path | None = None
) -> tuple[list[Result], dict[str, bytes]]:
    namespace = namespace_for(environment)
    results: list[Result] = []
    manifest, manifest_result = _read_manifest(directory)
    results.append(manifest_result)
    if manifest is None:
        return results, {}

    directory_mode_ok = True
    if os.name != "nt":
        try:
            directory_mode_ok = stat.S_IMODE(directory.stat().st_mode) & 0o077 == 0
        except OSError:
            directory_mode_ok = False
    results.append(
        Result(
            "secret_namespace_directory",
            str(directory.resolve(strict=False)),
            directory_mode_ok,
            "restricted ACL/mode" if directory_mode_ok else "permissions must be 0700 or stricter",
        )
    )

    expected_fields = {
        "schema_version": SCHEMA_VERSION,
        "environment": environment,
        "secret_namespace": namespace,
        "legacy_user_auth": False,
        "preserve_master_key_with_database": True,
    }
    for field, expected in expected_fields.items():
        actual = manifest.get(field)
        results.append(
            Result(
                field,
                "production-config.json",
                actual == expected,
                "matches production contract" if actual == expected else "missing or mismatched",
            )
        )
    backend_database = manifest.get("backend_database")
    backend_ok = isinstance(backend_database, str) and bool(backend_database.strip())
    results.append(
        Result(
            "backend_database",
            "production-config.json",
            backend_ok,
            "Account token key preservation required with retained database" if backend_ok else "missing",
        )
    )
    provider_database = manifest.get("provider_database")
    provider_ok = isinstance(provider_database, str) and bool(provider_database.strip())
    results.append(
        Result(
            "provider_database",
            "production-config.json",
            provider_ok,
            "master key preservation required with retained database" if provider_ok else "missing",
        )
    )

    files = manifest.get("secret_files")
    if not isinstance(files, dict):
        results.append(Result("secret_files", "production-config.json", False, "missing mapping"))
        return results, {}
    file_map_ok = set(files) == set(SECRET_SPECS)
    results.append(
        Result(
            "secret_file_allowlist",
            "production-config.json",
            file_map_ok,
            "exact required secret set" if file_map_ok else "missing or unexpected secret entry",
        )
    )
    values: dict[str, bytes] = {}
    for setting, (kind, _) in SECRET_SPECS.items():
        expected_name = _secret_name(namespace, setting)
        filename = files.get(setting)
        path = directory / str(filename)
        source = str(path.resolve(strict=False))
        valid = filename == expected_name
        reason = "namespace-scoped source" if valid else "wrong environment or filename"
        if valid and (path.is_symlink() or path.parent.resolve() != directory.resolve()):
            valid, reason = False, "source must be a regular file inside its namespace"
        try:
            raw = path.read_bytes() if valid else b""
            value = raw.decode("ascii").rstrip("\r\n")
        except (OSError, UnicodeError):
            valid, reason, value, raw = False, "missing or unreadable", "", b""
        if valid:
            value_error = _validate_value(setting, value, kind)
            if value_error:
                valid, reason = False, value_error
            elif os.name != "nt" and stat.S_IMODE(path.stat().st_mode) & 0o077:
                valid, reason = False, "permissions must be 0600 or stricter"
        if valid:
            values[setting] = value.encode("ascii")
        results.append(Result(setting, source, valid, reason if not valid else "strong and readable"))

    duplicates = {
        setting
        for setting, value in values.items()
        if sum(candidate == value for candidate in values.values()) > 1
    }
    for setting in sorted(duplicates):
        filename = files.get(setting, "")
        results.append(
            Result(
                setting,
                str((directory / str(filename)).resolve(strict=False)),
                False,
                "value reused by another credential role",
            )
        )
    distinct = len(values) == len(SECRET_SPECS) and not duplicates
    results.append(
        Result("secret_role_separation", str(directory.resolve()), distinct, "all values distinct" if distinct else "duplicate or incomplete values")
    )

    mounts = manifest.get("container_mounts")
    actual_mounts: set[tuple[str, str, str, bool]] = set()
    if isinstance(mounts, list):
        for mount in mounts:
            if isinstance(mount, dict):
                actual_mounts.add(
                    (
                        str(mount.get("secret", "")),
                        str(mount.get("service", "")),
                        str(mount.get("target", "")),
                        mount.get("read_only") is True,
                    )
                )
    mount_ok = actual_mounts == _expected_mounts(namespace)
    results.append(
        Result("container_mounts", "production-config.json", mount_ok, "least-privilege read-only mounts" if mount_ok else "mount allowlist mismatch")
    )

    if other_directory is not None:
        other_manifest, _ = _read_manifest(other_directory)
        other_values: set[bytes] = set()
        opposite = "Release" if environment == "Test" else "Test"
        opposite_namespace = namespace_for(opposite)
        isolated = bool(other_manifest) and other_manifest.get("environment") == opposite
        if isolated and isinstance(other_manifest.get("secret_files"), dict):
            for setting in SECRET_SPECS:
                expected_name = _secret_name(opposite_namespace, setting)
                if other_manifest["secret_files"].get(setting) != expected_name:
                    isolated = False
                    continue
                try:
                    other_values.add((other_directory / expected_name).read_bytes().strip())
                except OSError:
                    isolated = False
        isolated = isolated and not (set(values.values()) & other_values)
        results.append(
            Result("environment_isolation", str(other_directory.resolve()), isolated, "namespaces and values are independent" if isolated else "cross-environment source or value reuse")
        )
    return results, values


def scan_disclosures(values: dict[str, bytes], artifacts: Iterable[Path]) -> list[Result]:
    results: list[Result] = []
    for artifact in artifacts:
        paths = [artifact]
        if artifact.is_dir():
            paths = [path for path in artifact.rglob("*") if path.is_file() and not path.is_symlink()]
        disclosed = False
        readable = True
        for path in paths:
            try:
                content = path.read_bytes()
            except OSError:
                readable = False
                continue
            if any(secret and secret in content for secret in values.values()):
                disclosed = True
        results.append(
            Result(
                "non_disclosure",
                str(artifact.resolve(strict=False)),
                readable and not disclosed,
                "no configured secret bytes found" if readable and not disclosed else "secret disclosure or unreadable input",
            )
        )
    return results


def _print_results(results: Iterable[Result], json_output: bool) -> bool:
    diagnostics = [result.diagnostic() for result in results]
    if json_output:
        print(json.dumps(diagnostics, indent=2, sort_keys=True))
    else:
        for item in diagnostics:
            state = "valid" if item["valid"] else "invalid"
            print(f"{item['setting']}: {state}; source={item['source']}; {item['reason']}")
    return all(result.valid for result in results)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    generate_parser = subparsers.add_parser("generate")
    generate_parser.add_argument("--environment", choices=ENVIRONMENTS, required=True)
    generate_parser.add_argument("--directory", type=Path, required=True)
    generate_parser.add_argument("--backend-database", required=True)
    generate_parser.add_argument("--provider-database", required=True)
    validate_parser = subparsers.add_parser("validate")
    validate_parser.add_argument("--environment", choices=ENVIRONMENTS, required=True)
    validate_parser.add_argument("--directory", type=Path, required=True)
    validate_parser.add_argument("--other-directory", type=Path)
    validate_parser.add_argument("--json", action="store_true")
    scan_parser = subparsers.add_parser("scan")
    scan_parser.add_argument("--environment", choices=ENVIRONMENTS, required=True)
    scan_parser.add_argument("--directory", type=Path, required=True)
    scan_parser.add_argument("--artifact", action="append", type=Path, required=True)
    scan_parser.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)

    try:
        if args.command == "generate":
            generate(args.environment, args.directory, args.backend_database, args.provider_database)
            print(f"generated protected {args.environment} namespace at {args.directory}")
            return 0
        results, values = validate(
            args.environment,
            args.directory,
            getattr(args, "other_directory", None),
        )
        if args.command == "scan" and all(result.valid for result in results):
            results.extend(scan_disclosures(values, args.artifact))
        return 0 if _print_results(results, args.json) else 1
    except (OSError, ValueError, RuntimeError) as error:
        # Errors identify only configuration sources/categories; secret values are never interpolated.
        print(f"configuration operation failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
