#!/usr/bin/env python3
"""Check the complete final evidence package against reviewed image approvals."""
import argparse
import hashlib
import json
from pathlib import Path

from prepare import DEFAULT_MANIFEST, approved_images


def validate(directory):
    expected = {}
    for line in (directory / "SHA256SUMS").read_text().splitlines():
        digest, name = line.split("  ", 1)
        path = Path(name)
        if path.is_absolute() or ".." in path.parts or name in expected:
            raise ValueError("Unsafe or duplicate artifact")
        if hashlib.sha256((directory / path).read_bytes()).hexdigest() != digest:
            raise ValueError("Artifact checksum mismatch")
        expected[name] = digest
    actual = {path.relative_to(directory).as_posix() for path in directory.rglob("*") if path.is_file() and path.name != "SHA256SUMS"}
    if set(expected) != actual:
        raise ValueError("Unlisted or missing evidence")
    summary = json.loads((directory / "summary.json").read_text())
    images, promotion = approved_images(DEFAULT_MANIFEST)
    if (summary.get("status") != "passed" or summary.get("accepted_images") != images or
            summary.get("promotion_manifest_sha256") != promotion or summary.get("high_critical_count") != 0 or
            summary.get("all_seven_services_healthy") is not True):
        raise ValueError("Final sign-off differs from reviewed package")
    stages = ("preflight", "security", "surface", "incidents", "backup", "restore", "soak", "accepted-backup", "rollback", "rate-limit")
    if any(summary.get(stage, {}).get("status") != "passed" or summary[stage].get("report") not in expected for stage in stages):
        raise ValueError("Required acceptance missing")
    scan = json.loads((directory / "scans/summary.json").read_text())
    if scan["status"] != "passed" or {key: row["image"] for key, row in scan["images"].items()} != images:
        raise ValueError("Scan image mismatch")
    for row in scan["images"].values():
        if row["high_critical_count"] or expected["scans/" + row["report"]] != row["sha256"]:
            raise ValueError("Image vulnerability gate failed")
    restored = json.loads((directory / "restore.json").read_text())
    soak = json.loads((directory / "soak.json").read_text())
    incidents = json.loads((directory / "incidents.json").read_text())
    crash = incidents.get("backend_crash", {})
    if (incidents.get("status") != "passed" or crash.get("status") != "passed" or
            not crash.get("automatic_restart_count_advanced") or crash.get("manual_compose_start_needed") is not False):
        raise ValueError("Actual application crash recovery was not verified")
    if (restored.get("status") != "passed" or not restored.get("all_rows_schema_ids_history_match_before_writers_start") or
            not restored.get("disposable_containers_networks_volumes_removed") or
            soak.get("status") != "passed" or soak.get("requested_seconds", 0) < 120 or
            not soak.get("known_runtime_secrets_absent_from_logs")):
        raise ValueError("Recovery/stability gate failed")
    return {"status": "passed", "artifacts": len(expected), "services": len(images), "high_critical_count": 0}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    try:
        print(json.dumps(validate(args.directory)))
    except Exception as error:
        raise SystemExit("Final evidence rejected: " + type(error).__name__)
