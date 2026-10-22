#!/usr/bin/env python3
"""Build and push the current five production images to the local registry."""

import json
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import urlopen


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from check_versions import VersionError, check  # noqa: E402
REGISTRY = "127.0.0.1:5000"
BUILDER = "stock-ai-t10"
SERVICES = {
    "backend": "docker/backend.Dockerfile",
    "agent-server": "docker/agent-server.Dockerfile",
    "llm-provider-server": "docker/llm-provider-server.Dockerfile",
    "middleware": "docker/middleware.Dockerfile",
    "nginx": "docker/nginx.Dockerfile",
}


def run(*args):
    return subprocess.run(args, cwd=ROOT, check=True, capture_output=True, text=True, timeout=1800)


def main():
    try:
        check()
    except VersionError as error:
        raise SystemExit(f"Incompatible service versions:\n{error}")
    revision = run("git", "rev-parse", "HEAD").stdout.strip()
    source_epoch = run("git", "show", "-s", "--format=%ct", "HEAD").stdout.strip()
    build_date = datetime.fromtimestamp(int(source_epoch), tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    version = "local-" + revision[:12]
    server_os = run("docker", "version", "--format", "{{.Server.Os}}").stdout.strip()
    if server_os != "linux":
        raise SystemExit(f"Docker server must use Linux containers; found {server_os!r}")
    run("docker", "buildx", "inspect", BUILDER, "--bootstrap")
    with urlopen(f"http://{REGISTRY}/v2/", timeout=5) as response:
        if response.status != 200:
            raise SystemExit(f"Local registry returned HTTP {response.status}")

    for service, dockerfile in SERVICES.items():
        tag = f"{REGISTRY}/stock-ai/{service}:{version}"
        metadata_path = Path("/tmp") / f"stock-ai-t10-{service}-build.json"
        if metadata_path.is_file():
            try:
                existing = json.loads(metadata_path.read_text(encoding="utf-8"))
                args = existing["buildx.build.provenance"]["invocation"]["parameters"]["args"]
                digest = existing["containerimage.digest"]
                if args.get("build-arg:REVISION") == revision and digest.startswith("sha256:") and len(digest) == 71:
                    print(f"{service}: reusing completed build {REGISTRY}/stock-ai/{service}@{digest}")
                    continue
            except (KeyError, OSError, json.JSONDecodeError, TypeError):
                pass
            metadata_path.unlink(missing_ok=True)
        run(
            "docker", "buildx", "build", "--builder", BUILDER, "--platform", "linux/amd64",
            "--file", dockerfile, "--tag", tag,
            "--build-arg", f"BUILD_DATE={build_date}",
            "--build-arg", f"REVISION={revision}",
            "--build-arg", f"VERSION={version}",
            "--build-arg", f"SOURCE_DATE_EPOCH={source_epoch}",
            "--provenance=mode=max", "--sbom=true", "--metadata-file", str(metadata_path), "--push", ".",
        )
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        digest = metadata.get("containerimage.digest", "")
        if not digest.startswith("sha256:") or len(digest) != 71:
            raise SystemExit(f"BuildKit did not report an immutable digest for {service}")
        print(f"{service}: {REGISTRY}/stock-ai/{service}@{digest}")


if __name__ == "__main__":
    main()
