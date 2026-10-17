#!/usr/bin/env python3
"""Install a fresh WSL/Linux release from offline images: local fixtures, or an external IdP with --idp-env."""
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


def verify_external_edge(idp, runtime):
    """The edge answers on RELEASE_APP_HOST and /oauth2/start hands the browser to the external IdP."""
    host = idp["RELEASE_APP_HOST"]
    hostname, _, port = host.partition(":")
    published = idp["RELEASE_BIND"].rpartition(":")[2]
    curl = ["curl", "-sS", "--max-time", "10", "--resolve", f"{hostname}:{port or 443}:127.0.0.1",
            "--cacert", str(runtime / "certs/fullchain.pem"), "-o", "/dev/null", "-w", "%{http_code} %{redirect_url}"]
    if published != (port or "443"):
        curl[4:6] = ["--connect-to", f"{hostname}:{port or 443}:127.0.0.1:{published}"]
    health = run(*curl, f"https://{host}/healthz").stdout.split(" ", 1)
    start = run(*curl, f"https://{host}/oauth2/start").stdout.split(" ", 1)
    if health[0] != "200" or start[0] != "302" or not start[1].startswith(idp["OIDC_AUTHORIZATION_URL"]):
        raise RuntimeError("Release edge did not serve /healthz or redirect /oauth2/start to the IdP")


def main(args):
    bundle = args.bundle.resolve()
    manifest = verify(bundle)
    if args.verify_only:
        print("Package checksums verified; Linux/amd64 release")
        return
    if bool(args.idp_env) != bool(args.oidc_client_secret_file):
        raise ValueError("--idp-env and --oidc-client-secret-file must be given together")
    codex_auth = None
    if args.codex_auth_file:
        codex_auth = args.codex_auth_file.expanduser().resolve()
        if not codex_auth.is_file() or not isinstance(json.loads(codex_auth.read_text()), dict):
            raise ValueError("--codex-auth-file must name a readable Codex auth.json")
    import cryptography  # Require the host dependency before any Docker mutation.
    runtime = args.runtime.expanduser().resolve()
    if runtime.exists() and any(runtime.iterdir()):
        raise ValueError("Runtime must be a new empty directory; existing deployment was not changed")
    info = json.loads(run("docker", "info", "--format", "{{json .}}").stdout)
    if info.get("OSType") != "linux" or info.get("Architecture") not in ("x86_64", "amd64"):
        raise ValueError("A Linux/amd64 Docker engine is required")
    with tarfile.open(bundle / "source.tar.gz") as archive:
        archive.extractall(bundle, filter="data")
    source = bundle / "source"
    sys.path.insert(0, str(source))
    sys.path.insert(0, str(source / "scripts/local-release"))
    from prepare import prepare, approved_images, read_external_idp, DEFAULT_MANIFEST
    idp = read_external_idp(args.idp_env) if args.idp_env else None
    bind_host, _, bind_port = (idp["RELEASE_BIND"] if idp else "127.0.0.1:8443").rpartition(":")
    for host, port in (("127.0.0.1", 5000), (bind_host, int(bind_port))):
        with socket.socket() as probe:
            try:
                probe.bind((host, port))
            except PermissionError:  # Privileged ports (e.g. 443) cannot be probed unprivileged; Docker still binds them.
                pass
    registry_name, volume_name = "stock-ai-release-registry", "stock-ai-release-registry-data"
    for kind, name in (("container", registry_name), ("volume", volume_name)):
        if subprocess.run(["docker", kind, "inspect", name], capture_output=True).returncode == 0:
            raise ValueError("Existing release registry found; no existing state was overwritten")
    if run("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=stock-ai-local-release").stdout.strip():
        raise ValueError("Existing Release containers found; fresh installation refused")
    for volume in ("backend_state", "agent_state", "llm_state"):
        if subprocess.run(["docker", "volume", "inspect", "stock-ai-local-release_" + volume], capture_output=True).returncode == 0:
            raise ValueError("Existing application data volume found; fresh installation refused")
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
    env_file = prepare(runtime=runtime, idp=idp, client_secret_file=args.oidc_client_secret_file)
    if codex_auth:
        from prepare import install_private
        target = runtime / "secrets/stock-ai-release_codex_auth.json"
        install_private(target, codex_auth.read_bytes(), os.getgid())
        with env_file.open("a") as output:
            output.write("CODEX_AUTH_FILE=" + str(target) + "\n")
    from accept import sync_bundle
    deployment = runtime / "deployment"
    sync_bundle(deployment)
    shutil.copytree(source / "docs/deployment", deployment / "docs/deployment", dirs_exist_ok=True)
    overlay = "compose.release.yaml" if idp else "compose.local-release.yaml"
    files = [deployment / "deploy/compose" / name for name in ("compose.yaml", overlay)]
    if codex_auth:
        files.append(deployment / "deploy/compose/compose.codex.yaml")
    compose = ["docker", "compose", "--env-file", str(env_file), "-f", str(files[0]), "-f", str(files[1])]
    if codex_auth:
        compose.extend(["-f", str(files[2])])
    if idp:
        # Runs in the Gateway image with the deployment's trust store; prints the URIs to register in the IdP.
        if subprocess.run(compose + ["run", "--rm", "--no-deps", "oidc-gateway", "preflight"]).returncode:
            raise ValueError("OIDC preflight failed; fix the IdP settings shown above, then install into a new runtime")
    subprocess.run(compose + ["up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "180"], check=True)
    if idp:
        verify_external_edge(idp, runtime)
    else:
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
    if idp:
        print(f"Installed: https://{idp['RELEASE_APP_HOST']} (external IdP {idp['OIDC_ISSUER']})")
        print(f"IdP client {idp['OIDC_CLIENT_ID']} must register redirect URI https://{idp['RELEASE_APP_HOST']}/oauth2/callback"
              f" and post-logout URI https://{idp['RELEASE_APP_HOST']}/")
        pending = runtime / "evidence/install/authenticated-acceptance.json"
        pending.parent.mkdir(parents=True, exist_ok=True)
        pending.write_text(json.dumps({"schema": "stock-ai.authenticated-acceptance.v1", "status": "pending",
            "reason": "A real login, authenticated APIs, Agent account lookup and successful tool execution remain unverified."}, indent=2) + "\n")
        print("Authenticated acceptance: pending; run source/scripts/release/verify_authenticated.py with a real login cookie jar")
        print("Finish with the login acceptance checklist in docs/deployment/remote-windows-release.md")
    else:
        print("Installed: https://app.localrelease:8443 (loopback-only local fixtures)")
    print("Private runtime: " + str(runtime))
    print("Fresh data and credentials; no source-machine database, secret or private key was transferred")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bundle", type=Path, default=Path(__file__).resolve().parent)
    parser.add_argument("--runtime", type=Path, default=Path.home() / "stock-ai-runtime")
    parser.add_argument("--verify-only", action="store_true")
    parser.add_argument("--codex-auth-file", type=Path, help="Optional local Codex auth.json to persist in this runtime")
    parser.add_argument("--idp-env", type=Path, help="External IdP settings (deploy/compose/release.env.example)")
    parser.add_argument("--oidc-client-secret-file", type=Path, help="File holding the IdP client secret")
    args = parser.parse_args()
    try:
        main(args)
    except Exception as error:
        reason = str(error) if type(error) is ValueError else type(error).__name__
        raise SystemExit("Release install stopped: " + reason + ". Existing data was not deleted. Inspect local Docker status; no credentials were printed.")
