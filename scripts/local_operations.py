#!/usr/bin/env python3
"""Non-interactive WSL/Linux operator workflows for the isolated Compose stack."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import copy
import json
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

try:
    from scripts.promotion_contract import REQUIRED_RESULTS
except ModuleNotFoundError:  # Direct ``python scripts/local_operations.py`` execution.
    from promotion_contract import REQUIRED_RESULTS


CORE_SERVICES = ("backend", "llm-provider-server", "agent-server", "middleware")
OPTIONAL_SERVICES = ("nginx", "idp")
ALLOWED_SERVICES = set(CORE_SERVICES + OPTIONAL_SERVICES)
STOP_ORDER = ("nginx", "middleware", "agent-server", "llm-provider-server", "backend", "idp")
IMAGE_VARIABLES = {
    "backend": "BACKEND_IMAGE",
    "llm-provider-server": "LLM_PROVIDER_SERVER_IMAGE",
    "agent-server": "AGENT_SERVER_IMAGE",
    "middleware": "MIDDLEWARE_IMAGE",
    "nginx": "NGINX_IMAGE",
}
PROMOTABLE_SERVICES = tuple(IMAGE_VARIABLES)
PROMOTION_IMAGE_SERVICES = set(IMAGE_VARIABLES)
TEST_STACK_IMAGE_SERVICES = set(IMAGE_VARIABLES)
REQUIRED_ACCEPTANCE_RESULTS = set(REQUIRED_RESULTS)
PROMOTION_SCHEMA = "stock-ai.promotion-manifest.v1"
DIGEST_RE = re.compile(r"^[^\s@]+@(?P<digest>sha256:[0-9a-f]{64})$")
SCHEMA_VERSION = 2
LOOPBACK_INGRESS = {
    "Test": {("127.0.0.1", 80), ("127.0.0.1", 443), ("::1", 80), ("::1", 443)},
    "Release": {("127.0.0.1", 8443), ("::1", 8443)},
}


class WorkflowError(RuntimeError):
    pass


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def backup_freshness(directory: Path | None, *, now: float | None = None) -> tuple[int | None, int]:
    """Return age/count for only checksum-verified encrypted backup artifacts."""
    if directory is None or not directory.is_dir():
        return None, 0
    valid: list[Path] = []
    for backup in directory.glob("*.sai-backup"):
        sidecar = Path(str(backup) + ".sha256")
        try:
            line = sidecar.read_text(encoding="utf-8").strip()
            match = re.fullmatch(r"([0-9a-fA-F]{64})\s{2}(.+)", line)
            if not match or match.group(2) != backup.name:
                continue
            if sha256_bytes(backup.read_bytes()) != match.group(1).lower():
                continue
            valid.append(backup)
        except OSError:
            continue
    if not valid:
        return None, 0
    newest = max(path.stat().st_mtime for path in valid)
    return max(0, int((time.time() if now is None else now) - newest)), len(valid)


def config_fingerprint(config: dict[str, Any], image_services: set[str]) -> str:
    """Hash normalized Compose config while excluding only promoted image refs."""
    normalized = copy.deepcopy(config)
    for name in image_services:
        service = normalized.get("services", {}).get(name)
        if isinstance(service, dict):
            service.pop("image", None)
    return sha256_bytes(json.dumps(normalized, sort_keys=True, separators=(",", ":")).encode())


def read_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, separator, value = line.partition("=")
        if not separator or not key.strip():
            raise WorkflowError("Compose env file contains an invalid assignment.")
        values[key.strip()] = value.strip().strip("\"'")
    return values


def run(
    command: list[str], *, check: bool = True, timeout: int = 60, env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    try:
        result = subprocess.run(command, check=False, capture_output=True, text=True, timeout=timeout, env=env)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise WorkflowError(f"Command unavailable or timed out: {Path(command[0]).name}.") from exc
    if check and result.returncode:
        # Docker/Compose output may contain interpolated secrets. Never relay it.
        raise WorkflowError(f"{Path(command[0]).name} command failed (exit {result.returncode}).")
    return result


class Operator:
    def __init__(self, env_file: Path, compose_files: list[Path], evidence_dir: Path, timeout: int):
        self.env_file = env_file.resolve(strict=True)
        self.compose_files = [path.resolve(strict=True) for path in compose_files]
        self.evidence_dir = evidence_dir.expanduser().resolve()
        self.timeout = timeout
        self.values = read_env(self.env_file)
        self.docker = os.environ.get("DOCKER", "docker")
        interpolation_names = set(self.values)
        for compose_file in self.compose_files:
            interpolation_names.update(
                re.findall(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)", compose_file.read_text(encoding="utf-8"))
            )
        self.compose_environment = {key: value for key, value in os.environ.items() if key not in interpolation_names}

    def compose_args(self, *args: str) -> list[str]:
        command = [self.docker, "compose", "--env-file", str(self.env_file)]
        for compose_file in self.compose_files:
            command.extend(("-f", str(compose_file)))
        return command + list(args)

    def compose_run(self, *args: str, check: bool = True, timeout: int | None = None) -> subprocess.CompletedProcess[str]:
        return run(
            self.compose_args(*args),
            check=check,
            timeout=timeout or self.timeout,
            env=self.compose_environment,
        )

    def compose_config(self) -> dict[str, Any]:
        result = self.compose_run("config", "--format", "json")
        try:
            config = json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            raise WorkflowError("Compose returned invalid configuration JSON.") from exc
        return config

    def write_evidence(self, name: str, payload: dict[str, Any]) -> Path:
        self.evidence_dir.mkdir(parents=True, exist_ok=True)
        destination = self.evidence_dir / name
        content = (json.dumps(payload, indent=2, sort_keys=True) + "\n").encode()
        fd, temporary = tempfile.mkstemp(prefix=f".{name}.", dir=self.evidence_dir)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(content)
            os.chmod(temporary, 0o600)
            os.replace(temporary, destination)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
        return destination

    def validate_config(self, config: dict[str, Any], *, require_digests: bool = False) -> list[str]:
        issues: list[str] = []
        services = config.get("services")
        if not isinstance(services, dict) or not set(CORE_SERVICES).issubset(services):
            return ["Compose config must define the four isolated application services."]
        unknown = sorted(set(services) - ALLOWED_SERVICES)
        if unknown:
            issues.append("Unsupported services detected; legacy or unreviewed deployment excluded.")
        environment = self.values.get("STOCK_AI_ENVIRONMENT", "")
        if environment not in ("Test", "Release"):
            issues.append("STOCK_AI_ENVIRONMENT must be Test or Release.")
        digest_gate = require_digests or environment == "Release"
        for name, service in services.items():
            if service.get("build"):
                issues.append(f"{name} has a build directive; deployed images must be prebuilt.")
            if service.get("network_mode"):
                issues.append(f"{name} uses an unsupported host network mode.")
            image = service.get("image", "")
            if not image:
                issues.append(f"{name} has no configured image.")
            elif digest_gate and not DIGEST_RE.fullmatch(image):
                issues.append(f"{name} image is mutable; {environment or 'this'} workflow requires a SHA256 digest.")
            for port in service.get("ports", []) or []:
                host = str(port.get("host_ip") or "")
                published = str(port.get("published") or "")
                if name != "nginx":
                    issues.append(f"{name} must not publish a host port; Nginx is the only ingress service.")
                    continue
                if host not in {"127.0.0.1", "::1"}:
                    issues.append("Nginx host ports must bind to IPv4 or IPv6 loopback only.")
                    continue
                try:
                    port_number = int(published)
                except ValueError:
                    issues.append("Nginx published port is outside the allowed local ingress contract.")
                    continue
                if (host, port_number) not in LOOPBACK_INGRESS.get(environment, set()):
                    issues.append("Nginx published port is outside the allowed local ingress contract.")
        return issues

    @staticmethod
    def _check_bind_sources(config: dict[str, Any], issues: list[str]) -> None:
        for service_name, service in config.get("services", {}).items():
            for volume in service.get("volumes", []) or []:
                if isinstance(volume, dict) and volume.get("type") == "bind":
                    source = volume.get("source")
                    if source and not Path(source).exists():
                        issues.append(f"{service_name} bind-mount source is missing.")
        for secret_name, secret in config.get("secrets", {}).items():
            source = secret.get("file") if isinstance(secret, dict) else None
            if source and not Path(source).is_file():
                issues.append(f"Required secret file is missing: {secret_name}.")

    @staticmethod
    def _check_ports(config: dict[str, Any], issues: list[str]) -> None:
        for service_name, service in config.get("services", {}).items():
            for port in service.get("ports", []) or []:
                try:
                    if service_name != "nginx" or port.get("host_ip") not in {"127.0.0.1", "::1"}:
                        continue
                    published_range = str(port.get("published", "0"))
                    bounds = [int(value) for value in published_range.split("-", maxsplit=1)]
                    first, last = (bounds[0], bounds[-1])
                    host = port.get("host_ip") or "0.0.0.0"
                    if first <= 0:
                        continue
                    if last < first or last > 65535:
                        raise ValueError("invalid published port range")
                    family = socket.AF_INET6 if ":" in host else socket.AF_INET
                    for published in range(first, last + 1):
                        with socket.socket(family, socket.SOCK_STREAM) as probe:
                            probe.bind((host, published))
                except (OSError, TypeError, ValueError):
                    issues.append(f"Published port for {service_name} is unavailable.")

    def preflight(self, promotion_manifest: Path | None = None) -> tuple[dict[str, Any], int]:
        issues: list[str] = []
        engine: dict[str, Any] = {}
        info = run([self.docker, "info", "--format", "{{json .}}"], check=False, timeout=self.timeout)
        if info.returncode:
            issues.append("Docker engine is unavailable.")
        else:
            try:
                engine = json.loads(info.stdout)
            except json.JSONDecodeError:
                issues.append("Docker engine returned invalid info.")
            if engine.get("OSType") != "linux":
                issues.append("Docker engine must use Linux containers.")
            if int(engine.get("NCPU", 0) or 0) < 2:
                issues.append("Docker engine must provide at least 2 CPUs.")
            if int(engine.get("MemTotal", 0) or 0) < 4 * 1024**3:
                issues.append("Docker engine must provide at least 4 GiB memory.")
        try:
            env_mode = self.env_file.stat().st_mode & 0o777
            if env_mode & 0o077:
                issues.append("Compose env file permissions must not grant group or other access.")
            config = self.compose_config()
            issues.extend(self.validate_config(config))
            if self.values.get("STOCK_AI_ENVIRONMENT") == "Release":
                if promotion_manifest is None:
                    issues.append("Release preflight requires the checksummed Test promotion manifest.")
                else:
                    try:
                        promoted, _ = self.promotion(promotion_manifest, config)
                        configured_images = self.image_map(config)
                        configured = {name: configured_images[name] for name in PROMOTABLE_SERVICES if name in configured_images}
                        promoted_images = {name: promoted[name] for name in PROMOTABLE_SERVICES if name in promoted}
                        if configured != promoted_images:
                            issues.append("Release application images differ from the checksummed promotion manifest.")
                    except (OSError, WorkflowError) as exc:
                        issues.append(str(exc))
            self._check_bind_sources(config, issues)
            self._check_ports(config, issues)
            disk = shutil.disk_usage(self.env_file.parent)
            if disk.free < 10 * 1024**3:
                issues.append("At least 10 GiB free space is required on the env-file filesystem.")
        except (OSError, WorkflowError) as exc:
            issues.append(str(exc))
        report = {
            "checked_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": self.values.get("STOCK_AI_ENVIRONMENT"),
            "engine": {"linux": engine.get("OSType") == "linux", "cpus": engine.get("NCPU"), "memory_bytes": engine.get("MemTotal")},
            "issues": issues,
            "passed": not issues,
            "windows_restart_and_ntfs": "not_run",
        }
        path = self.write_evidence("preflight.json", report)
        report["evidence"] = str(path)
        return report, 0 if not issues else 1

    def image_map(self, config: dict[str, Any]) -> dict[str, str]:
        services = config["services"]
        return {name: str(services[name]["image"]) for name in PROMOTABLE_SERVICES if name in services}

    def compose_inputs_fingerprint(self) -> str:
        inputs = [
            {"path": str(path), "sha256": sha256_bytes(path.read_bytes())}
            for path in self.compose_files
        ]
        return sha256_bytes(json.dumps(inputs, sort_keys=True, separators=(",", ":")).encode())

    def non_image_env_fingerprint(self) -> str:
        values = read_env(self.env_file)
        non_image_values = {key: value for key, value in values.items() if key not in set(IMAGE_VARIABLES.values())}
        return sha256_bytes(json.dumps(non_image_values, sort_keys=True, separators=(",", ":")).encode())

    def container(self, service: str) -> dict[str, Any] | None:
        result = self.compose_run("ps", "--all", "-q", service, check=False)
        container_id = result.stdout.strip()
        if result.returncode or not container_id:
            return None
        inspected = run([self.docker, "inspect", "--size", "--format", "{{json .}}", container_id], check=False)
        if inspected.returncode:
            return None
        try:
            return json.loads(inspected.stdout)
        except json.JSONDecodeError:
            return None

    def diagnostics(self) -> dict[str, Any]:
        config = self.compose_config()
        results = []
        for service in config["services"]:
            container = self.container(service)
            if not container:
                results.append({"service": service, "state": "missing"})
                continue
            state = container.get("State", {})
            health = state.get("Health", {}).get("Status", "none")
            results.append({
                "service": service,
                "container_id": str(container.get("Id", ""))[:12],
                "state": state.get("Status", "unknown"),
                "health": health,
                "exit_code": state.get("ExitCode"),
                "restart_count": container.get("RestartCount", 0),
                "error_present": bool(state.get("Error")),
            })
        return {"captured_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(), "services": results}

    def wait_healthy(self, config: dict[str, Any]) -> None:
        required = list(config["services"])
        deadline = time.monotonic() + self.timeout
        states: dict[str, str] = {}
        while time.monotonic() < deadline:
            pending = []
            for service in required:
                container = self.container(service)
                if not container:
                    states[service] = "missing"
                    pending.append(service)
                    continue
                state = container.get("State", {})
                health = state.get("Health", {}).get("Status", "no-healthcheck")
                states[service] = f"{state.get('Status', 'unknown')}/{health}"
                if state.get("Status") != "running" or health != "healthy":
                    pending.append(service)
            if not pending:
                return
            time.sleep(min(2, max(0, deadline - time.monotonic())))
        diagnostic = self.write_evidence("diagnostics.json", self.diagnostics())
        detail = ", ".join(f"{service}={states.get(service, 'unknown')}" for service in required)
        raise WorkflowError(f"Health wait failed for {detail}. Diagnostics: {diagnostic}")

    def start(self, promotion_manifest: Path | None = None) -> dict[str, Any]:
        report, status = self.preflight(promotion_manifest)
        if status:
            raise WorkflowError(f"Preflight failed; see {report['evidence']}.")
        config = self.compose_config()
        self.compose_run("up", "-d", "--no-build")
        try:
            self.wait_healthy(config)
        except WorkflowError:
            raise
        return {"started": True, "services": list(config["services"]), "health": "healthy"}

    @staticmethod
    def _cert_expiry(path: Path) -> str | None:
        result = run(["openssl", "x509", "-enddate", "-noout", "-in", str(path)], check=False)
        if result.returncode or "=" not in result.stdout:
            return None
        value = result.stdout.strip().split("=", 1)[1]
        try:
            return dt.datetime.strptime(value, "%b %d %H:%M:%S %Y %Z").replace(tzinfo=dt.timezone.utc).isoformat()
        except ValueError:
            return None

    def status(self) -> dict[str, Any]:
        config = self.compose_config()
        services = []
        images = self.image_map(config)
        for name in config["services"]:
            container = self.container(name)
            state = container.get("State", {}) if container else {}
            health = state.get("Health", {}).get("Status", "none") if container else "missing"
            services.append({
                "service": name,
                "image_reference": config["services"][name].get("image"),
                "digest": (DIGEST_RE.fullmatch(str(config["services"][name].get("image", ""))) or {}).group("digest") if DIGEST_RE.fullmatch(str(config["services"][name].get("image", ""))) else None,
                "state": state.get("Status", "missing") if container else "missing",
                "health": health,
                "restart_count": container.get("RestartCount", 0) if container else 0,
                "writable_bytes": container.get("SizeRw") if container else None,
            })
        certificates = []
        for raw in self.values.get("STOCK_AI_CERTIFICATE_FILES", "").split(os.pathsep):
            if raw:
                path = Path(raw)
                try:
                    expiry = self._cert_expiry(path) if path.is_file() else None
                except WorkflowError:
                    expiry = None
                certificates.append({"present": path.is_file(), "expires_at_utc": expiry})
        backup_dir = Path(self.values["STOCK_AI_BACKUP_DIR"]) if self.values.get("STOCK_AI_BACKUP_DIR") else None
        backup_age, backup_count = backup_freshness(backup_dir)
        issuer = self.values.get("OIDC_ISSUER", "").rstrip("/")
        idp_reachable = None
        if issuer:
            try:
                with urllib.request.urlopen(issuer + "/.well-known/openid-configuration", timeout=5) as response:
                    idp_reachable = 200 <= response.status < 400
            except (urllib.error.URLError, TimeoutError, ssl.SSLError, OSError):
                idp_reachable = False
        disk = run([self.docker, "system", "df", "--format", "{{.Type}}|{{.Size}}"], check=False)
        disk_usage = []
        if not disk.returncode:
            for line in disk.stdout.splitlines():
                kind, separator, size = line.partition("|")
                if separator:
                    disk_usage.append({"type": kind, "size": size})
        return {
            "captured_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
            "environment": self.values.get("STOCK_AI_ENVIRONMENT"),
            "project": config.get("name"),
            "services": services,
            "docker_disk_usage": disk_usage,
            "certificate_expiry": certificates,
            "idp_reachable": idp_reachable,
            "backup_age_seconds": backup_age,
            "verified_backup_count": backup_count,
            "windows_restart_and_ntfs": "not_run",
        }

    def stop(self) -> dict[str, Any]:
        config = self.compose_config()
        stopped = []
        for service in STOP_ORDER:
            if service in config["services"]:
                self.compose_run("stop", "--timeout", "30", service)
                stopped.append(service)
        remaining = []
        for service in config["services"]:
            container = self.container(service)
            if container and container.get("State", {}).get("Status") == "running":
                remaining.append(service)
        if remaining:
            raise WorkflowError("Containers remain running after graceful stop: " + ", ".join(remaining))
        return {"stopped_in_order": stopped, "running_services": remaining, "volumes_removed": False}

    @staticmethod
    def _artifact_ref(value: Any) -> tuple[str, str] | None:
        if not isinstance(value, dict):
            return None
        file_name, digest = value.get("file"), value.get("sha256")
        if not isinstance(file_name, str) or not isinstance(digest, str):
            return None
        if not re.fullmatch(r"[0-9a-fA-F]{64}", digest):
            return None
        return file_name, digest.lower()

    @staticmethod
    def _walk_artifact_refs(value: Any, prefix: str = "") -> list[tuple[str, str, str]]:
        refs: list[tuple[str, str, str]] = []
        if isinstance(value, dict):
            ref = Operator._artifact_ref(value)
            if ref:
                refs.append((prefix, ref[0], ref[1]))
            for key, nested in value.items():
                if key not in {"file", "sha256"}:
                    refs.extend(Operator._walk_artifact_refs(nested, f"{prefix}.{key}".strip(".")))
        elif isinstance(value, list):
            for index, nested in enumerate(value):
                refs.extend(Operator._walk_artifact_refs(nested, f"{prefix}[{index}]"))
        return refs

    def _verify_manifest_files(self, manifest_path: Path, manifest: dict[str, Any]) -> str:
        if manifest_path.name != "promotion.json":
            raise WorkflowError("Promotion manifest must be named promotion.json.")
        root = manifest_path.parent.resolve(strict=True)
        sums_path = root / "SHA256SUMS"
        if not sums_path.is_file():
            raise WorkflowError("Promotion manifest SHA256SUMS is missing.")
        entries: dict[str, list[str]] = {}
        for line in sums_path.read_text(encoding="utf-8").splitlines():
            found = re.fullmatch(r"([0-9a-fA-F]{64})\s+\*?(.+)", line)
            if found:
                entries.setdefault(found.group(2), []).append(found.group(1).lower())
        required: list[tuple[str, str]] = [("promotion.json", sha256_bytes(manifest_path.read_bytes()))]
        artifact_refs = self._walk_artifact_refs(manifest)
        if not artifact_refs:
            raise WorkflowError("Promotion manifest does not reference evidence artifacts.")
        for field, relative_name, expected_hash in artifact_refs:
            relative = Path(relative_name)
            if relative.is_absolute() or ".." in relative.parts:
                raise WorkflowError("Promotion manifest contains an unsafe artifact path.")
            artifact_path = (root / relative).resolve(strict=True)
            if not artifact_path.is_relative_to(root) or not artifact_path.is_file():
                raise WorkflowError("Promotion manifest artifact is missing or outside its directory.")
            actual_hash = sha256_bytes(artifact_path.read_bytes())
            if actual_hash != expected_hash:
                raise WorkflowError(f"Promotion artifact checksum failed: {field}.")
            required.append((relative.as_posix(), expected_hash))
        for relative_name, expected_hash in required:
            if entries.get(relative_name) != [expected_hash]:
                raise WorkflowError("SHA256SUMS must contain one matching entry for every promotion artifact.")
        return required[0][1]

    def promotion(self, path: Path, config: dict[str, Any]) -> tuple[dict[str, str], str]:
        manifest_path = path.resolve(strict=True)
        try:
            manifest = json.loads(manifest_path.read_bytes())
        except json.JSONDecodeError as exc:
            raise WorkflowError("Promotion manifest is invalid JSON.") from exc
        if not isinstance(manifest, dict):
            raise WorkflowError("Promotion manifest root must be a JSON object.")
        if (manifest.get("schema") != PROMOTION_SCHEMA or manifest.get("status") != "promoted"
                or manifest.get("environment") != "Test"):
            raise WorkflowError("Promotion manifest schema, status, or environment is invalid.")
        revision = manifest.get("git_revision")
        if not isinstance(revision, str) or not re.fullmatch(r"[0-9a-fA-F]{40,64}", revision):
            raise WorkflowError("Promotion manifest must contain a full Git revision.")
        if manifest.get("configuration_schema_version") in (None, "", 0):
            raise WorkflowError("Promotion manifest configuration schema version is missing.")
        if manifest.get("trust") != {"mechanism": "SHA-256 sidecar", "checksum_file": "SHA256SUMS"}:
            raise WorkflowError("Promotion manifest trust declaration is invalid.")
        acceptance_records = manifest.get("acceptance_results")
        if not isinstance(acceptance_records, dict):
            raise WorkflowError("Promotion manifest acceptance_results must be a dictionary.")
        images = manifest.get("images")
        if not isinstance(images, dict) or set(images) != PROMOTION_IMAGE_SERVICES:
            raise WorkflowError("Promotion manifest must contain exactly the five images.")
        if any(not isinstance(ref, str) or not DIGEST_RE.fullmatch(ref) for ref in images.values()):
            raise WorkflowError("Every promoted image must use repository@sha256:<64 lowercase hex>.")
        test_images = manifest.get("test_stack_images")
        if not isinstance(test_images, dict) or set(test_images) != TEST_STACK_IMAGE_SERVICES:
            raise WorkflowError("Promotion test_stack_images must contain the five images.")
        if any(not isinstance(ref, str) or not DIGEST_RE.fullmatch(ref) for ref in test_images.values()):
            raise WorkflowError("Every Test stack image must use an immutable SHA256 digest.")
        if any(test_images[name] != images[name] for name in PROMOTION_IMAGE_SERVICES):
            raise WorkflowError("Test stack images differ from certified application images.")
        artifacts = manifest.get("artifacts")
        if not isinstance(artifacts, dict) or set(artifacts) != TEST_STACK_IMAGE_SERVICES:
            raise WorkflowError("Promotion artifacts must cover the five images.")
        for service in TEST_STACK_IMAGE_SERVICES:
            artifact = artifacts[service]
            expected_digest = images.get(service, test_images[service])
            expected_build_digest = expected_digest.rsplit("@", 1)[1]
            if not isinstance(artifact, dict) or artifact.get("build_digest") != expected_build_digest:
                raise WorkflowError(f"Promotion artifact build digest mismatch for {service}.")
            for artifact_name in ("sbom", "scan", "filesystem_inventory"):
                if not self._artifact_ref(artifact.get(artifact_name)):
                    raise WorkflowError(f"Promotion artifact is missing {artifact_name} for {service}.")
            registry_reference = artifact.get("registry_reference")
            if not isinstance(registry_reference, str) or not registry_reference.strip():
                raise WorkflowError(f"Promotion artifact is missing registry_reference for {service}.")
        for field in ("build_provenance", "build_acceptance"):
            if not self._artifact_ref(manifest.get(field)):
                raise WorkflowError(f"Promotion manifest top-level {field} file/hash is missing.")
        certification = manifest.get("certification_evidence")
        if not self._artifact_ref(certification):
            raise WorkflowError("Promotion certification evidence file/hash is missing.")
        results = certification.get("results")
        if not isinstance(results, dict):
            raise WorkflowError("Certification evidence results must be a dictionary.")
        if set(results) != set(acceptance_records):
            raise WorkflowError("Certification result names do not match acceptance result names.")
        if set(acceptance_records) != REQUIRED_ACCEPTANCE_RESULTS or set(results) != REQUIRED_ACCEPTANCE_RESULTS:
            raise WorkflowError("Certification evidence must cover the exact required acceptance checks.")
        if any(
            (entry.get("status") if isinstance(entry, dict) else entry) != "passed"
            for entry in acceptance_records.values()
        ):
            raise WorkflowError("Promotion manifest must contain all required passed acceptance results.")
        result_refs = [self._artifact_ref(record) for record in results.values()]
        if (
            any(ref is None for ref in result_refs)
            or len({ref[0] for ref in result_refs if ref}) != len(REQUIRED_ACCEPTANCE_RESULTS)
        ):
            raise WorkflowError("Certification evidence must contain distinct checksummed refs for all required checks.")
        checksum = self._verify_manifest_files(manifest_path, manifest)
        promoted = {name: ref for name, ref in images.items()
                    if name in CORE_SERVICES or name in config.get("services", {})}
        return promoted, checksum

    def change_images(self, images: dict[str, str]) -> None:
        lines = self.env_file.read_text(encoding="utf-8").splitlines()
        wanted = {IMAGE_VARIABLES[name]: reference for name, reference in images.items()}
        seen: set[str] = set()
        for index, line in enumerate(lines):
            match = re.match(r"^\s*([A-Z0-9_]+)\s*=", line)
            if match and match.group(1) in wanted:
                key = match.group(1)
                lines[index] = f"{key}={wanted[key]}"
                seen.add(key)
        if seen != set(wanted):
            raise WorkflowError("Compose env file is missing one or more image assignments; no update applied.")
        mode = self.env_file.stat().st_mode & 0o777
        fd, temporary = tempfile.mkstemp(prefix=f".{self.env_file.name}.", dir=self.env_file.parent)
        try:
            with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as stream:
                stream.write("\n".join(lines) + "\n")
            os.chmod(temporary, mode)
            os.replace(temporary, self.env_file)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def update(self, manifest_path: Path) -> dict[str, Any]:
        config = self.compose_config()
        preflight_issues = self.validate_config(config, require_digests=True)
        # A Test stack may be running on temporary tags before promotion.
        preflight_issues = [issue for issue in preflight_issues if "image is mutable" not in issue]
        if preflight_issues:
            raise WorkflowError("Compose deployment policy failed: " + "; ".join(preflight_issues))
        promoted, manifest_hash = self.promotion(manifest_path, config)
        previous = self.image_map(config)
        normalized_config_hash = config_fingerprint(config, set(promoted))
        compose_inputs_hash = self.compose_inputs_fingerprint()
        non_image_env_hash = self.non_image_env_fingerprint()
        metadata = {
            "schema_version": SCHEMA_VERSION,
            "created_at_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
            "project": config.get("name"),
            "environment": self.values.get("STOCK_AI_ENVIRONMENT"),
            "compose_inputs_sha256": compose_inputs_hash,
            "normalized_compose_sha256": normalized_config_hash,
            "non_image_env_sha256": non_image_env_hash,
            "promotion_manifest_sha256": manifest_hash,
            "previous_images": previous,
            "promoted_images": promoted,
        }
        rollback_key = sha256_bytes(
            (str(config.get("name")) + manifest_hash + non_image_env_hash + compose_inputs_hash + normalized_config_hash).encode()
        )[:16]
        metadata_path = self.write_evidence(f"rollback-{rollback_key}.json", metadata)
        self.change_images(promoted)
        try:
            updated = self.compose_config()
            issues = self.validate_config(updated, require_digests=True)
            if issues:
                raise WorkflowError("Promoted Compose config failed policy validation.")
            self.compose_run("pull")
            self.compose_run("up", "-d", "--no-build")
            self.wait_healthy(updated)
        except Exception as update_error:
            self.change_images(previous)
            try:
                self.compose_run("up", "-d", "--no-build", check=False)
                self.wait_healthy(self.compose_config())
            except Exception as rollback_error:
                raise WorkflowError(
                    f"Update failed and automatic rollback health could not be confirmed; "
                    f"metadata: {metadata_path}; reason: {type(rollback_error).__name__}."
                ) from update_error
            raise
        return {"updated": True, "rollback_metadata": str(metadata_path), "image_services": sorted(promoted)}

    def rollback(self, metadata_path: Path) -> dict[str, Any]:
        try:
            metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise WorkflowError("Rollback metadata is unavailable or invalid.") from exc
        config = self.compose_config()
        expected = {name for name in PROMOTABLE_SERVICES if name in config["services"]}
        previous = metadata.get("previous_images")
        promoted = metadata.get("promoted_images")
        if (metadata.get("schema_version") != SCHEMA_VERSION or metadata.get("project") != config.get("name")
                or not isinstance(previous, dict) or set(previous) != expected
                or not isinstance(promoted, dict) or set(promoted) != expected
                or self.image_map(config) != promoted):
            raise WorkflowError("Rollback metadata does not match this Compose project.")
        if (
            metadata.get("compose_inputs_sha256") != self.compose_inputs_fingerprint()
            or metadata.get("normalized_compose_sha256") != config_fingerprint(config, expected)
            or metadata.get("non_image_env_sha256") != self.non_image_env_fingerprint()
        ):
            raise WorkflowError("Rollback metadata is stale: Compose inputs/config or non-image env values changed.")
        if any(not isinstance(ref, str) or not ref for ref in previous.values()):
            raise WorkflowError("Rollback metadata contains an invalid previous image map.")
        if any(not DIGEST_RE.fullmatch(ref) for ref in promoted.values()):
            raise WorkflowError("Rollback metadata promoted image map is not immutable.")
        if self.values.get("STOCK_AI_ENVIRONMENT") == "Release" and any(
            not DIGEST_RE.fullmatch(ref) for ref in previous.values()
        ):
            raise WorkflowError("Release rollback metadata contains a mutable previous image.")
        self.change_images(previous)
        rolled_back = self.compose_config()
        self.compose_run("up", "-d", "--no-build")
        self.wait_healthy(rolled_back)
        return {"rolled_back": True, "project": config.get("name"), "images": previous}


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("preflight", "start", "status", "stop", "update", "rollback", "diagnose"))
    parser.add_argument("--env-file", required=True, type=Path)
    parser.add_argument("-f", "--compose-file", action="append", required=True, type=Path)
    parser.add_argument("--evidence-dir", type=Path, default=Path("operator-evidence"))
    parser.add_argument("--promotion-manifest", type=Path)
    parser.add_argument("--rollback-metadata", type=Path)
    parser.add_argument("--timeout", type=int, default=180)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv or sys.argv[1:])
    try:
        operator = Operator(args.env_file, args.compose_file, args.evidence_dir, args.timeout)
        if args.action == "preflight":
            report, code = operator.preflight(args.promotion_manifest)
            print(json.dumps(report, indent=2, sort_keys=True))
            return code
        if args.action == "start":
            report = operator.start(args.promotion_manifest)
        elif args.action == "status":
            report = operator.status()
            operator.write_evidence("status.json", report)
        elif args.action == "stop":
            report = operator.stop()
        elif args.action == "diagnose":
            report = operator.diagnostics()
            operator.write_evidence("diagnostics.json", report)
        elif args.action == "update":
            if not args.promotion_manifest:
                raise WorkflowError("Update requires --promotion-manifest.")
            report = operator.update(args.promotion_manifest)
        else:
            if not args.rollback_metadata:
                raise WorkflowError("Rollback requires --rollback-metadata.")
            report = operator.rollback(args.rollback_metadata)
        print(json.dumps(report, indent=2, sort_keys=True))
        return 0
    except (WorkflowError, OSError, ValueError) as exc:
        print(f"operator workflow failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
