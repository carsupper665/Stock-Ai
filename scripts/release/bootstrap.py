#!/usr/bin/env python3
"""Install a fresh, loopback-only WSL/Linux fixture release from offline images."""
import argparse
import base64
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tarfile
import time
import urllib.request
from pathlib import Path


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True)


def verify(bundle):
    listed = set()
    for line in (bundle / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split("  ", 1)
        if Path(name).name != name or name in listed or (bundle / name).is_symlink():
            raise ValueError("Invalid package inventory")
        if hashlib.sha256((bundle / name).read_bytes()).hexdigest() != digest:
            raise ValueError("Package checksum mismatch")
        listed.add(name)
    required = {"bootstrap.py", "README.zh-TW.md", "manifest.json", "source.tar.gz", "registry-image.tar.gz", "registry-data.tar.gz"}
    if listed != required:
        raise ValueError("Incomplete package inventory")
    manifest = json.loads((bundle / "manifest.json").read_text())
    if manifest.get("schema") != "stock-ai.portable-release.v1" or manifest.get("platform") != "linux/amd64":
        raise ValueError("Unsupported release package")
    return manifest


def main(args):
    bundle = args.bundle.resolve()
    manifest = verify(bundle)
    if args.verify_only:
        print("Package checksums verified; Linux/amd64 fixture release")
        return
    import cryptography  # Require the host dependency before any Docker mutation.
    runtime = args.runtime.expanduser().resolve()
    if runtime.exists() and any(runtime.iterdir()):
        raise ValueError("Runtime must be a new empty directory; existing deployment was not changed")
    info = json.loads(run("docker", "info", "--format", "{{json .}}").stdout)
    if info.get("OSType") != "linux" or info.get("Architecture") not in ("x86_64", "amd64"):
        raise ValueError("A Linux/amd64 Docker engine is required")
    for port in (5000, 8443):
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", port))
    registry_name, volume_name = "stock-ai-release-registry", "stock-ai-release-registry-data"
    for kind, name in (("container", registry_name), ("volume", volume_name)):
        if subprocess.run(["docker", kind, "inspect", name], capture_output=True).returncode == 0:
            raise ValueError("Existing release registry found; no existing state was overwritten")
    if run("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=stock-ai-local-release").stdout.strip():
        raise ValueError("Existing Release containers found; fresh installation refused")
    for volume in ("backend_state", "agent_state", "llm_state"):
        if subprocess.run(["docker", "volume", "inspect", "stock-ai-local-release_" + volume], capture_output=True).returncode == 0:
            raise ValueError("Existing application data volume found; fresh installation refused")
    with tarfile.open(bundle / "source.tar.gz") as archive:
        archive.extractall(bundle, filter="data")
    source = bundle / "source"
    sys.path.insert(0, str(source))
    sys.path.insert(0, str(source / "scripts/local-release"))
    from prepare import prepare, approved_images, DEFAULT_MANIFEST
    images, promotion = approved_images(DEFAULT_MANIFEST)
    if images != manifest["images"] or promotion != manifest["promotion_manifest_sha256"]:
        raise ValueError("Packaged source approvals differ from bundled images")
    run("docker", "image", "load", "--input", str(bundle / "registry-image.tar.gz"))
    image = manifest["registry_image"]
    if run("docker", "image", "inspect", image, "--format", "{{.Id}}").stdout.strip() != manifest["registry_image_id"]:
        raise ValueError("Registry helper image ID mismatch")
    run("docker", "volume", "create", volume_name)
    run("docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
        "--mount", f"type=volume,src={volume_name},dst=/var/lib/registry",
        "--mount", f"type=bind,src={bundle / 'registry-data.tar.gz'},dst=/backup.tar.gz,readonly",
        "--entrypoint", "/bin/tar", image, "-xzf", "/backup.tar.gz", "-C", "/var/lib/registry", "--no-same-owner")
    run("docker", "run", "-d", "--name", registry_name, "--restart", "unless-stopped", "--read-only",
        "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--publish", "127.0.0.1:5000:5000",
        "--mount", f"type=volume,src={volume_name},dst=/var/lib/registry", image)
    for attempt in range(30):
        try:
            with urllib.request.urlopen("http://127.0.0.1:5000/v2/", timeout=2):
                break
        except OSError:
            if attempt == 29:
                raise RuntimeError("Local release registry did not start")
            time.sleep(1)
    for service, reference in sorted(images.items()):
        print("Loading verified image: " + service, flush=True)
        run("docker", "pull", reference)
        run("docker", "image", "inspect", reference)
    run("docker", "stop", registry_name)
    env_file = prepare(runtime=runtime)
    from accept import sync_bundle
    deployment = runtime / "deployment"
    sync_bundle(deployment)
    shutil.copytree(source / "docs/deployment", deployment / "docs/deployment", dirs_exist_ok=True)
    files = [deployment / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    compose = ["docker", "compose", "--env-file", str(env_file), "-f", str(files[0]), "-f", str(files[1])]
    subprocess.run(compose + ["up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "180"], check=True)
    from verify_ingress import verify as ingress
    if ingress(runtime)["status"] != "passed":
        raise RuntimeError("Verified TLS/login acceptance failed")
    from deploy import snapshot, project_containers
    from scripts.local_operations import Operator
    with os.fdopen(os.open(runtime / "recovery.key", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as key:
        key.write(base64.b64encode(os.urandom(32)) + b"\n")
    operator = Operator(env_file, files, runtime / "evidence/install", 180)
    backup = snapshot(operator, operator.compose_config(), project_containers(), runtime / "backups", runtime / "recovery.key")
    operator.write_evidence("initial-backup.json", backup)
    print("Installed: https://app.localrelease:8443 (loopback-only local fixtures)")
    print("Private runtime: " + str(runtime))
    print("Fresh data and credentials; no source-machine database, secret or private key was transferred")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, default=Path(__file__).resolve().parent)
    parser.add_argument("--runtime", type=Path, default=Path.home() / "stock-ai-runtime")
    parser.add_argument("--verify-only", action="store_true")
    args = parser.parse_args()
    try:
        main(args)
    except Exception as error:
        reason = str(error) if type(error) is ValueError else type(error).__name__
        raise SystemExit("Release install stopped: " + reason + ". Existing data was not deleted. Inspect local Docker status; no credentials were printed.")
