#!/usr/bin/env python3
"""Preflight, encrypted snapshot, immutable Release startup and restart acceptance."""

import argparse
import json
import shutil
import socket
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from lint import lint, SERVICES
from prepare import DEFAULT_MANIFEST, INSTANCE, ROOT, approved_images, persistent_runtime
from verify_ingress import verify as verify_ingress
from verify_runtime import verify as verify_runtime
from scripts import linux_recovery as recovery
from scripts.local_operations import Operator, SCHEMA_VERSION, WorkflowError, config_fingerprint, run


def utc():
    return datetime.now(timezone.utc)


def persistent(path):
    """Reject runtime and configuration that a worktree merge can remove."""
    return persistent_runtime(path)


def port_check(incumbents):
    try:
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 8443))
        return "free"
    except OSError:
        publishers = [entry for entry in incumbents if any(
            binding.get("HostIp") == "127.0.0.1" and binding.get("HostPort") == "8443"
            for bindings in entry["NetworkSettings"]["Ports"].values() for binding in bindings or [])]
        if len(publishers) != 1 or publishers[0]["Config"]["Labels"]["com.docker.compose.service"] != "nginx":
            raise WorkflowError("Release loopback port unavailable or not owned by its Nginx")
        return "owned-by-existing-release-nginx"


def project_containers():
    ids = run(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={INSTANCE}"]).stdout.split()
    return json.loads(run(["docker", "inspect", *ids]).stdout) if ids else []


def preflight(operator, manifest):
    deployment = operator.compose_files[0].parents[2]
    bundle = ("deploy/compose/compose.yaml", "deploy/compose/compose.local-release.yaml",
              "deploy/nginx/release-nginx.conf", "deploy/nginx/release-edge.conf.template",
              "deploy/nginx/release-fixture-idp.conf")
    if any((deployment / name).is_symlink() or (deployment / name).read_bytes() != (ROOT / name).read_bytes()
           for name in bundle):
        raise WorkflowError("Persistent deployment bundle differs from reviewed checkout")
    lint(operator.env_file, manifest, operator.compose_files)
    config = operator.compose_config()
    info = json.loads(run(["docker", "info", "--format", "{{json .}}"] ).stdout)
    if info.get("OSType") != "linux" or info.get("NCPU", 0) < 2 or info.get("MemTotal", 0) < 4 * 1024**3:
        raise WorkflowError("Linux engine resources insufficient")
    host_time = utc()
    engine_time = datetime.fromisoformat(info["SystemTime"].replace("Z", "+00:00"))
    skew = abs((host_time - engine_time).total_seconds())
    if host_time.year < 2026 or skew > 60:
        raise WorkflowError("Host/engine clock sanity failed")
    disk = shutil.disk_usage(operator.env_file.parent)
    if disk.free < 10 * 1024**3:
        raise WorkflowError("Runtime filesystem has less than 10 GiB free")
    images, checksum = approved_images(manifest)
    for image in images.values():
        run(["docker", "image", "inspect", image])
    # Probe the engine's actual volume filesystem rather than assuming its
    # storage is on the WSL checkout filesystem.
    storage = run(["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--mount",
                   f"type=volume,src={INSTANCE}_backend_state,dst=/data,readonly",
                   "--entrypoint", "/bin/df", images["nginx"], "-Pk", "/data"]).stdout
    engine_disk_free = int(storage.splitlines()[-1].split()[3]) * 1024
    if engine_disk_free < 10 * 1024**3:
        raise WorkflowError("Engine volume filesystem has less than 10 GiB free")
    issues = []
    operator._check_bind_sources(config, issues)
    if issues:
        raise WorkflowError("Required deployment files missing")
    for service in config["services"].values():
        for mount in service.get("volumes", []):
            if mount.get("type") == "bind":
                persistent(Path(mount["source"]))
    for definition in config["secrets"].values():
        persistent(Path(definition["file"]))
    incumbents = project_containers()
    if incumbents and {entry["Config"]["Labels"]["com.docker.compose.service"] for entry in incumbents} != SERVICES:
        raise WorkflowError("Incumbent Release service set differs")
    if any(entry["Config"]["Image"] != images[entry["Config"]["Labels"]["com.docker.compose.service"]] for entry in incumbents):
        raise WorkflowError("Incumbent digest differs from certified package")
    # This profile configures only its local IdP and fake provider. No external
    # API credential or registry connection is needed: all approved images exist.
    return {"schema": "stock-ai.release-preflight.v1", "status": "passed", "checked_at_utc": host_time.isoformat(),
            "engine": {"linux": True, "cpus": info["NCPU"], "memory_bytes": info["MemTotal"]},
            "runtime_disk_free_bytes": disk.free, "engine_volume_disk_free_bytes": engine_disk_free,
            "clock_skew_seconds": round(skew, 3),
            "loopback_port": port_check(incumbents), "required_files": "passed",
            "deployment_bundle_matches_reviewed_checkout": True,
            "approved_images_present": sorted(images), "promotion_manifest_sha256": checksum,
            "outbound_dependencies": {"external": [], "local": ["OIDC fixture", "fake provider"],
                                      "registry": "not-required-pull-never"}}, config, incumbents


def snapshot(operator, config, incumbents, backup_dir, key_file):
    """Pause existing containers and encrypt all volumes plus complete runtime."""
    backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    if backup_dir.is_symlink() or backup_dir.stat().st_mode & 0o077:
        raise WorkflowError("Backup directory must be private")
    key = recovery.recovery_key(key_file)
    stamp = utc().strftime("%Y%m%dT%H%M%SZ")
    destination = backup_dir / f"{INSTANCE}-{stamp}.sai-backup"
    helper = config["services"]["nginx"]["image"]
    paused = []
    try:
        for entry in incumbents:
            if entry["State"]["Running"]:
                if entry["State"].get("Paused"):
                    raise WorkflowError("Incumbent already paused")
                run(["docker", "pause", entry["Id"]])
                paused.append(entry["Id"])
        with tempfile.TemporaryDirectory(dir=backup_dir) as temporary:
            base = Path(temporary)
            base.chmod(0o700)
            stage = base / "contents"
            stage.mkdir(mode=0o700)
            for service, volume in recovery.VOLUMES.items():
                archive = base / (service + ".tar.gz")
                name = config["volumes"][volume]["name"]
                run(["docker", "volume", "inspect", name])
                command = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
                           "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--mount",
                           f"type=volume,src={name},dst=/data,readonly", "--entrypoint", "/bin/tar", helper,
                           "-czf", "-", "-C", "/data", "."]
                with archive.open("wb") as output:
                    response = subprocess.run(command, stdout=output, stderr=subprocess.PIPE, timeout=60)
                if response.returncode:
                    raise WorkflowError("Volume snapshot failed")
                target = stage / service
                target.mkdir(mode=0o700)
                recovery.extract_archive(archive, target)
            runtime = operator.env_file.parent
            for directory in ("secrets", "certs"):
                shutil.copytree(runtime / directory, stage / directory)
            shutil.copyfile(operator.env_file, stage / "compose.env")
            shutil.copytree(operator.compose_files[0].parents[2] / "deploy", stage / "deploy")
            facts = {service: recovery.inspect_sqlite(service, stage / service / database)
                     for service, database in recovery.DATABASES.items()}
            manifest = {"schema": "stock-ai.complete-release-backup.v1", "created_at_utc": utc().isoformat(),
                        "consistency": "all-running-project-containers-paused", "files": recovery.inventory(stage),
                        "images": {name: service["image"] for name, service in config["services"].items()}}
            (stage / "manifest.json").write_text(json.dumps(manifest))
            plain = base / "snapshot.tar.gz"
            recovery.build_archive(stage, plain)
            recovery.encrypt_file(plain, destination, key)
            digest = recovery.write_checksum(destination)
            recovery.verify_checksum(destination)
            decrypted = base / "verify.tar.gz"
            recovery.decrypt_file(destination, decrypted, key)
            verified = base / "verified"
            verified.mkdir(mode=0o700)
            recovery.extract_archive(decrypted, verified)
            if recovery.inventory(verified) != manifest["files"]:
                raise WorkflowError("Backup decrypted inventory differs")
            return {"schema": manifest["schema"], "backup_file": str(destination), "sha256": digest,
                    "created_at_utc": manifest["created_at_utc"], "encrypted": "AES-256-GCM",
                    "authenticated_and_inventory_verified": True, "databases": facts,
                    "all_nine_secrets_and_tls_jwks": True, "consistency": manifest["consistency"]}
    finally:
        for identifier in reversed(paused):
            run(["docker", "unpause", identifier])


def rollback_metadata(operator, config, manifest):
    promoted, checksum = operator.promotion(manifest, config)
    return {"schema_version": SCHEMA_VERSION, "created_at_utc": utc().isoformat(), "project": config["name"],
            "environment": "Release", "compose_inputs_sha256": operator.compose_inputs_fingerprint(),
            "normalized_compose_sha256": config_fingerprint(config, set(promoted)),
            "non_image_env_sha256": operator.non_image_env_fingerprint(), "promotion_manifest_sha256": checksum,
            "previous_images": operator.image_map(config), "promoted_images": promoted}


def deploy(args):
    runtime, deployment = persistent(args.runtime), persistent(args.deployment_root)
    persistent(args.backup_dir)
    persistent(args.key_file)
    persistent(args.evidence_dir)
    persistent(args.operator_script)
    files = [deployment / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
    report, config, incumbents = preflight(operator, args.manifest)
    operator.write_evidence("preflight.json", report)
    if args.preflight_only:
        return report
    backup = snapshot(operator, config, incumbents, args.backup_dir, args.key_file)
    operator.write_evidence("backup.json", backup)
    metadata = rollback_metadata(operator, config, args.manifest)
    metadata_path = operator.write_evidence("rollback.json", metadata)
    # No pull and no build: every approved digest was inspected before mutation.
    operator.compose_run("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "180", timeout=240)
    operator.wait_healthy(config)
    operator.write_evidence("runtime.json", verify_runtime(args.test_project))
    operator.write_evidence("ingress.json", verify_ingress(runtime))
    before = operator.container("backend")
    operator.compose_run("restart", "--no-deps", "backend")
    operator.wait_healthy(config)
    after = operator.container("backend")
    if before["Id"] != after["Id"] or before["State"]["StartedAt"] == after["State"]["StartedAt"]:
        raise WorkflowError("Single-container restart not observed")
    operator.write_evidence("restart-runtime.json", verify_runtime(args.test_project))
    operator.write_evidence("restart-ingress.json", verify_ingress(runtime))
    with socket.create_connection(("127.0.0.1", 8443), timeout=5):
        pass
    result = {"schema": "stock-ai.release-deployment.v1", "status": "passed", "completed_at_utc": utc().isoformat(),
              "image_policy": "exact-promoted-digests-no-build-pull-never", "fingerprints": metadata,
              "backup_sha256": backup["sha256"], "single_backend_restart": "recovered-healthy-same-container",
              "host_loopback_socket": "connected", "published_ingress": "Nginx-only-127.0.0.1:8443",
              "rollback_command": __import__("shlex").join(["python3", str(args.operator_script.resolve()), "rollback",
                 "--env-file", str(operator.env_file), "-f", str(files[0]), "-f", str(files[1]),
                 "--evidence-dir", str(args.evidence_dir.resolve()), "--rollback-metadata", str(metadata_path)]),
              "limitations": ["public DNS/TLS not run non-gating", "host reboot not run non-gating",
                              "TLS/JWKS fixture keys replaced after T11 disposable worktree removal; nine rescued secrets preserved"]}
    operator.write_evidence("deployment.json", result)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--deployment-root", required=True, type=Path)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--backup-dir", type=Path, required=True)
    parser.add_argument("--key-file", type=Path, required=True)
    parser.add_argument("--test-project", default="stock-ai-t10-cert")
    parser.add_argument("--operator-script", type=Path, required=True,
                        help="Persistent checkout path to scripts/local_operations.py")
    parser.add_argument("--preflight-only", action="store_true")
    args = parser.parse_args()
    try:
        print(json.dumps(deploy(args), sort_keys=True))
    except Exception as error:
        args.evidence_dir.mkdir(parents=True, exist_ok=True)
        (args.evidence_dir / "failure.json").write_text(json.dumps({"status": "failed", "stage_error_type": type(error).__name__,
            "checked_at_utc": utc().isoformat(), "reason": "Private diagnostics suppressed; no completion claimed"}) + "\n")
        raise SystemExit("Release deployment failed closed; inspect sanitized evidence")
