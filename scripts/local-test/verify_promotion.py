#!/usr/bin/env python3
"""Exercise the Release digest gate against the final promotion package."""

import argparse
import hashlib
import json
import re
from datetime import datetime, timezone
from pathlib import Path

from verify_release import ReleaseVerificationError, verify_manifest_bytes, verify_release_configuration


SERVICES = ("backend", "agent-server", "llm-provider-server", "middleware", "nginx")


def write_result(directory, name, details):
    record = {
        "schema": "stock-ai.local-release-gate-result.v1",
        "name": name,
        "status": "passed",
        "tested_at_utc": datetime.now(timezone.utc).isoformat(),
        "details": details,
    }
    destination = directory / f"{name}.json"
    destination.write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return hashlib.sha256(destination.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    arguments = parser.parse_args()
    manifest_path = arguments.manifest.resolve()
    sidecar = manifest_path.parent / "SHA256SUMS"
    records = {}
    for line in sidecar.read_text(encoding="ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (.+)", line)
        if match:
            records[match.group(2)] = match.group(1)
    raw = manifest_path.read_bytes()
    verify_manifest_bytes(raw, records.get(manifest_path.name, ""))
    manifest = json.loads(raw)
    compose = {"services": {name: {"image": manifest["images"][name]} for name in SERVICES}}
    accepted = verify_release_configuration(manifest, compose)

    output = arguments.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)
    result_hashes = {}
    result_hashes["release_exact_digest"] = write_result(output, "release_exact_digest", {
        "manifest_sha256": hashlib.sha256(raw).hexdigest(),
        "services": list(accepted),
        "exact_test_digests": True,
    })

    substituted = json.loads(json.dumps(compose))
    image = substituted["services"]["backend"]["image"]
    digest = image.rsplit("@", 1)[1]
    other = "0" * 64 if digest != "sha256:" + "0" * 64 else "1" * 64
    substituted["services"]["backend"]["image"] = image.rsplit("@", 1)[0] + "@sha256:" + other
    try:
        verify_release_configuration(manifest, substituted)
    except ReleaseVerificationError:
        result_hashes["release_manifest_substitution"] = write_result(
            output, "release_manifest_substitution", {"substitution_rejected": True, "service": "backend"})
    else:
        raise SystemExit("Release gate accepted a substituted image digest")

    try:
        verify_manifest_bytes(raw.replace(b"promoted", b"tampered", 1), records[manifest_path.name])
    except ReleaseVerificationError:
        result_hashes["release_manifest_tamper"] = write_result(
            output, "release_manifest_tamper", {"manifest_tamper_rejected": True, "checksum": "SHA-256"})
    else:
        raise SystemExit("Release gate accepted a tampered promotion manifest")

    built = json.loads(json.dumps(compose))
    built["services"]["backend"]["build"] = {"context": "."}
    try:
        verify_release_configuration(manifest, built)
    except ReleaseVerificationError:
        result_hashes["release_build_directive"] = write_result(
            output, "release_build_directive", {"build_directive_rejected": True, "service": "backend"})
    else:
        raise SystemExit("Release gate accepted a Compose build directive")

    lines = [f"{digest}  {name}.json\n" for name, digest in sorted(result_hashes.items())]
    (output / "SHA256SUMS").write_text("".join(lines), encoding="ascii")
    print(f"Release digest gate passed against {manifest_path}; checksummed evidence: {output}")


if __name__ == "__main__":
    main()
