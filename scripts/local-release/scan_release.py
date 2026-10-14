#!/usr/bin/env python3
"""Scan the exact running Release images; never print container environments."""

import argparse
import hashlib
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from prepare import approved_images, DEFAULT_MANIFEST
from scripts.local_operations import DIGEST_RE

TRIVY = "aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa"


def scan(output, nginx_candidate=None):
    output.mkdir(parents=True, exist_ok=True)
    images, manifest_hash = approved_images(DEFAULT_MANIFEST)
    if nginx_candidate:
        if not DIGEST_RE.fullmatch(nginx_candidate):
            raise ValueError("Candidate must be an immutable Nginx image")
        images["nginx"] = nginx_candidate
    records = {}
    for service, reference in sorted(images.items()):
        print(f"Scanning {service}", flush=True)
        path = output / f"{service}.json"
        saved = path.read_bytes() if path.is_file() else b""
        if saved and json.loads(saved).get("ArtifactName") == reference:
            raw = saved
        else:
            result = subprocess.run([
                "docker", "run", "--rm", "--volume", "/var/run/docker.sock:/var/run/docker.sock:ro",
                "--volume", "stock-ai-t10-trivy-cache:/root/.cache/trivy", TRIVY,
                "image", "--quiet", "--image-src", "docker", "--scanners", "vuln",
                "--severity", "HIGH,CRITICAL", "--format", "json", reference,
            ], capture_output=True, timeout=600)
            if result.returncode:
                raise RuntimeError(f"Scanner failed for {service}; no passing result recorded")
            raw = result.stdout
            path.write_bytes(raw)
        report = json.loads(raw)
        vulnerabilities = [finding for component in report.get("Results", [])
                           for finding in component.get("Vulnerabilities", [])]
        records[service] = {
            "image": reference, "report": path.name,
            "sha256": hashlib.sha256(raw).hexdigest(),
            "high_critical_count": len(vulnerabilities),
            "findings": [{key: finding.get(key) for key in
                          ("VulnerabilityID", "PkgName", "InstalledVersion", "FixedVersion", "Severity")}
                         for finding in vulnerabilities],
        }
        print(f"{service}: {len(vulnerabilities)} High/Critical", flush=True)
    summary = {"schema": "stock-ai.local-release-scan.v1",
               "status": "passed" if all(not row["high_critical_count"] for row in records.values()) else "failed",
               "completed_at_utc": datetime.now(timezone.utc).isoformat(),
               "scanner_image": TRIVY, "promotion_manifest_sha256": manifest_hash, "images": records}
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    return summary


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--nginx-candidate")
    args = parser.parse_args()
    result = scan(args.output, args.nginx_candidate)
    raise SystemExit(0 if result["status"] == "passed" else 1)
