#!/usr/bin/env python3
"""Capture checksummed SBOM, vulnerability scans and flattened image inventories."""

import hashlib
import json
import re
import subprocess
import tarfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / ".local-test"
OUTPUT = RUNTIME / "build-evidence"
SERVICES = ("backend", "agent-server", "llm-provider-server", "console", "oidc-gateway")
TRIVY = "aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa"
CACHE_VOLUME = "stock-ai-t10-trivy-cache"


def run(*args, stdout=None):
    if stdout is None:
        return subprocess.run(args, check=True, capture_output=True, text=True, timeout=900)
    return subprocess.run(args, check=True, stdout=stdout, stderr=subprocess.PIPE, text=True, timeout=900)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    references = json.loads((RUNTIME / "registry-references.json").read_text(encoding="utf-8"))
    revision = run("git", "rev-parse", "HEAD").stdout.strip()
    tree = run("git", "rev-parse", "HEAD^{tree}").stdout.strip()
    source_date_epoch = int(run("git", "show", "-s", "--format=%ct", "HEAD").stdout.strip())
    existing_provenance = OUTPUT / "provenance.json"
    if existing_provenance.is_file():
        try:
            prior_revision = json.loads(existing_provenance.read_text(encoding="utf-8")).get("revision")
        except (OSError, json.JSONDecodeError):
            prior_revision = None
        if prior_revision != revision:
            for path in OUTPUT.iterdir():
                if path.is_file():
                    path.unlink()
    metadata = {}
    image_records = {}
    run("docker", "pull", TRIVY)
    run("docker", "volume", "create", CACHE_VOLUME)
    scanner_mounts = ("--volume", "/var/run/docker.sock:/var/run/docker.sock", "--volume", f"{CACHE_VOLUME}:/root/.cache/trivy")
    for service in SERVICES:
        ref = references[service]
        build_metadata = json.loads((Path("/tmp") / f"stock-ai-t10-{service}-build.json").read_text(encoding="utf-8"))
        if build_metadata.get("containerimage.digest") != ref.rsplit("@", 1)[1]:
            raise SystemExit(f"Registry digest differs from BuildKit metadata for {service}")
        prov = build_metadata.get("buildx.build.provenance", {})
        if prov.get("invocation", {}).get("parameters", {}).get("args", {}).get("build-arg:REVISION") != revision:
            raise SystemExit(f"Build provenance revision differs from HEAD for {service}")
        metadata[service] = build_metadata
        run("docker", "pull", ref)

        sbom = OUTPUT / f"{service}-sbom.spdx.json"
        if not sbom.is_file() or sbom.stat().st_size == 0:
            with sbom.open("w", encoding="utf-8") as stream:
                run("docker", "run", "--rm", *scanner_mounts, TRIVY,
                    "image", "--quiet", "--image-src", "docker", "--format", "spdx-json", ref, stdout=stream)
        scan = OUTPUT / f"{service}-vulnerabilities.sarif"
        if not scan.is_file() or scan.stat().st_size == 0:
            with scan.open("w", encoding="utf-8") as stream:
                run("docker", "run", "--rm", *scanner_mounts, TRIVY,
                    "image", "--quiet", "--image-src", "docker", "--scanners", "vuln", "--severity", "HIGH,CRITICAL",
                    "--exit-code", "1", "--format", "sarif", ref, stdout=stream)
        report = json.loads(scan.read_text(encoding="utf-8"))
        findings = [finding for item in report.get("runs", []) for finding in item.get("results", [])]
        if findings:
            raise SystemExit(f"Critical/High findings found for {service}: {len(findings)}")

        inventory = OUTPUT / f"{service}-files.txt"
        if not inventory.is_file() or inventory.stat().st_size == 0:
            container = f"stock-ai-t10-inventory-{service}"
            archive = OUTPUT / f".{service}.tar"
            run("docker", "create", "--name", container, ref)
            try:
                run("docker", "export", "--output", str(archive), container)
                with tarfile.open(archive) as layer:
                    paths = sorted({member.name.lstrip("./").rstrip("/") + ("/" if member.isdir() else "")
                                    for member in layer.getmembers() if member.name not in (".", "./")})
                inventory.write_text("\n".join(paths) + "\n", encoding="utf-8")
            finally:
                run("docker", "rm", container)
                archive.unlink(missing_ok=True)
        else:
            paths = inventory.read_text(encoding="utf-8").splitlines()
        text = "\n".join(paths).lower()
        sensitive_path = re.compile(
            r"(^|/)(?:\.env(?:\..*)?|secrets?/[^/]+|credentials?/[^/]+|"
            r"[^/]+\.(?:db|sqlite|pem|p12|pfx|key|log)|"
            r"[^/]*(?:secret|credential|session|token)[^/]*\.(?:env|json|txt|pem|key|db))$"
        )
        if "stock-ai-test" in text or sensitive_path.search(text):
            raise SystemExit(f"Test material or sensitive runtime path found in {service} filesystem")
        image_records[service] = {"build_result_digest": ref.rsplit("@", 1)[1]}

    (OUTPUT / "provenance.json").write_text(json.dumps({
        "schema": "stock-ai.local-build-provenance.v1",
        "revision": revision,
        "source_tree": tree,
        "source_date_epoch": source_date_epoch,
        "platform": "linux/amd64",
        "builder": "Docker Buildx / BuildKit stock-ai-t10",
        "images": {name: {"digest": data["containerimage.digest"], "registry_reference": references[name], "provenance": data["buildx.build.provenance"]} for name, data in metadata.items()},
    }, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (OUTPUT / "acceptance-summary.json").write_text(json.dumps({
        "status": "pass",
        "revision": revision,
        "source_tree": tree,
        "critical_high_findings": 0,
        "images": image_records,
        "checks": {"sbom": "passed", "vulnerability_scan": "passed", "flattened_filesystem_inventory": "passed"},
    }, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    files = sorted(path for path in OUTPUT.iterdir() if path.is_file())
    sums = "".join(f"{digest(path)}  {path.name}\n" for path in files)
    (OUTPUT / "SHA256SUMS").write_text(sums, encoding="ascii")
    print(f"Build evidence captured and checksummed under {OUTPUT}")


if __name__ == "__main__":
    main()
