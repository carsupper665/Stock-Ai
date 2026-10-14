#!/usr/bin/env python3
"""Create a checksummed Release promotion manifest from Test evidence.

This command is deliberately fail closed. It does not build, tag, or push images;
the supplied registry references must identify the exact images accepted on Test.
"""

import argparse
import hashlib
import json
import re
import shutil
import sys
from pathlib import Path


TEST_IMAGES = (
    "backend",
    "agent-server",
    "llm-provider-server",
    "console",
    "oidc-gateway",
)
RELEASE_IMAGES = TEST_IMAGES[:-1]
REQUIRED_RESULTS = (
    "secured_product_flow",
    "wrong_user",
    "expired_session",
    "idp_denial",
    "idp_restart",
    "internal_service_restart",
    "docker_restart_failure",
    "test_material_absence",
    "issuer_mismatch",
    "audience_mismatch",
    "fixture_outage",
    "public_login_redirects",
    "release_exact_digest",
    "release_manifest_substitution",
    "release_manifest_tamper",
    "release_build_directive",
)
IMMUTABLE_REFERENCE = re.compile(r"^[^\s@]+@sha256:[0-9a-f]{64}$")
REVISION = re.compile(r"^[0-9a-f]{40}$")


class PromotionError(ValueError):
    pass


def read_json(path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise PromotionError(f"cannot read JSON input {path}: {error}") from error


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def verify_checksums(directory):
    checksum_file = directory / "SHA256SUMS"
    if not checksum_file.is_file():
        raise PromotionError(f"missing evidence checksum file: {checksum_file}")
    recorded = {}
    for line in checksum_file.read_text(encoding="ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (.+)", line)
        if not match or Path(match.group(2)).name != match.group(2) or match.group(2) in recorded:
            raise PromotionError("evidence SHA256SUMS contains an invalid or duplicate entry")
        recorded[match.group(2)] = match.group(1)
    for name, expected in recorded.items():
        path = directory / name
        if not path.is_file() or path.is_symlink() or sha256(path) != expected:
            raise PromotionError(f"evidence checksum mismatch: {name}")
    return recorded


def require_artifact(directory, records, filename):
    path = directory / filename
    if filename not in records or not path.is_file() or path.stat().st_size == 0:
        raise PromotionError(f"missing checksummed build artifact: {filename}")
    return {"file": filename, "sha256": records[filename]}


def require_clean_inventory(directory, records, filename):
    artifact = require_artifact(directory, records, filename)
    inventory = (directory / filename).read_text(encoding="utf-8").lower().splitlines()
    forbidden = re.compile(
        r"(^|/)(?:\.env(?:\..*)?|secrets?/[^/]+|credentials?/[^/]+|"
        r"[^/]+\.(?:db|sqlite|pem|p12|pfx|key|log)|"
        r"[^/]*(?:secret|credential|session|token)[^/]*\.(?:env|json|txt|pem|key|db))$"
    )
    hits = [entry for entry in inventory if forbidden.search(entry)]
    if "stock-ai-test" in "\n".join(inventory) or hits:
        raise PromotionError(f"Test material or sensitive runtime path found in {filename}")
    return artifact


def create_manifest(build_dir, certification_path, references_path):
    records = verify_checksums(build_dir)
    provenance = read_json(build_dir / "provenance.json")
    build_result = read_json(build_dir / "acceptance-summary.json")
    certification = read_json(certification_path)
    references = read_json(references_path)
    certification_records = verify_checksums(certification_path.parent)
    if certification_records.get(certification_path.name) != sha256(certification_path):
        raise PromotionError("Test certification is not sealed by its local SHA256SUMS")

    revision = provenance.get("revision")
    if not isinstance(revision, str) or not REVISION.fullmatch(revision):
        raise PromotionError("build provenance has no full Git revision")
    if build_result.get("status") != "pass" or build_result.get("revision") != revision:
        raise PromotionError("production image acceptance does not pass for the provenance revision")
    if build_result.get("critical_high_findings") != 0:
        raise PromotionError("production image acceptance reports Critical/High findings")
    if certification.get("schema") != "stock-ai.test-certification.v1":
        raise PromotionError("unsupported Test certification schema")
    if certification.get("status") != "passed" or certification.get("revision") != revision:
        raise PromotionError("Test certification is not passed for the production revision")
    configuration_schema = certification.get("configuration_schema_version")
    if not isinstance(configuration_schema, str) or not configuration_schema.strip():
        raise PromotionError("Test certification must record the configuration schema version")
    results = certification.get("results")
    if not isinstance(results, dict) or any(results.get(name) != "passed" for name in REQUIRED_RESULTS):
        raise PromotionError("Test certification is missing a required passing flow or failure-path result")
    evidence = certification.get("evidence")
    if not isinstance(evidence, dict) or set(evidence) != set(REQUIRED_RESULTS):
        raise PromotionError("Test certification must reference sanitized evidence for every acceptance result")
    evidence_files = []
    for name, record in evidence.items():
        if not isinstance(record, dict):
            raise PromotionError(f"Test certification evidence record is invalid: {name}")
        filename = record.get("file")
        expected_hash = record.get("sha256")
        if not isinstance(filename, str) or Path(filename).name != filename or not re.fullmatch(r"[0-9a-f]{64}", str(expected_hash)):
            raise PromotionError(f"Test certification evidence reference is invalid: {name}")
        evidence_files.append(filename)
        evidence_path = certification_path.parent / filename
        if not evidence_path.is_file() or evidence_path.is_symlink() or sha256(evidence_path) != expected_hash:
            raise PromotionError(f"Test certification evidence hash mismatch: {filename}")
        if certification_records.get(filename) != expected_hash:
            raise PromotionError(f"Test acceptance evidence is not sealed by SHA256SUMS: {filename}")
    if len(evidence_files) != len(set(evidence_files)):
        raise PromotionError("Test acceptance results must retain distinct evidence files")

    if set(references) != set(TEST_IMAGES):
        raise PromotionError("registry references must contain exactly the five Test stack images")
    accepted_digests = certification.get("accepted_image_digests")
    if not isinstance(accepted_digests, dict) or set(accepted_digests) != set(TEST_IMAGES):
        raise PromotionError("Test certification must record accepted digests for all five stack images")
    for service, reference in references.items():
        if not isinstance(reference, str) or not IMMUTABLE_REFERENCE.fullmatch(reference):
            raise PromotionError(f"{service} is not an immutable SHA256 registry reference")
        registry_digest = reference.rsplit("@", 1)[1]
        build_digest = build_result.get("images", {}).get(service, {}).get("build_result_digest")
        if accepted_digests[service] != registry_digest or build_digest != registry_digest:
            raise PromotionError(f"{service} registry digest differs from the Test-accepted build digest")

    artifacts = {}
    for service in TEST_IMAGES:
        artifacts[service] = {
            "build_digest": build_result.get("images", {}).get(service, {}).get("build_result_digest"),
            "sbom": require_artifact(build_dir, records, f"{service}-sbom.spdx.json"),
            "scan": require_artifact(build_dir, records, f"{service}-vulnerabilities.sarif"),
            "filesystem_inventory": require_clean_inventory(build_dir, records, f"{service}-files.txt"),
            "provenance": require_artifact(build_dir, records, "provenance.json"),
            "acceptance": require_artifact(build_dir, records, "acceptance-summary.json"),
            "registry_reference": references[service],
        }
        if not artifacts[service]["build_digest"]:
            raise PromotionError(f"build acceptance is missing the {service} image digest")

    return {
        "schema": "stock-ai.promotion-manifest.v1",
        "status": "promoted",
        "environment": "Test",
        "git_revision": revision,
        "configuration_schema_version": configuration_schema,
        "images": {service: references[service] for service in RELEASE_IMAGES},
        "test_stack_images": {service: references[service] for service in TEST_IMAGES},
        "artifacts": artifacts,
        "acceptance_results": {name: results[name] for name in REQUIRED_RESULTS},
        "certification_evidence": {
            "file": certification_path.name,
            "sha256": sha256(certification_path),
            "results": evidence,
        },
        "build_provenance": require_artifact(build_dir, records, "provenance.json"),
        "build_acceptance": require_artifact(build_dir, records, "acceptance-summary.json"),
        "trust": {"mechanism": "SHA-256 sidecar", "checksum_file": "SHA256SUMS"},
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build-evidence", required=True, type=Path)
    parser.add_argument("--test-certification", required=True, type=Path)
    parser.add_argument("--registry-references", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args(argv)
    try:
        manifest = create_manifest(
            args.build_evidence.resolve(),
            args.test_certification.resolve(),
            args.registry_references.resolve(),
        )
        args.output.parent.mkdir(parents=True, exist_ok=True)
        sidecar_path = args.output.parent / "SHA256SUMS"
        if args.output.exists() or sidecar_path.exists():
            raise PromotionError("refusing to overwrite an existing promotion output")
        package_files = {}
        source_files = [
            args.test_certification.resolve(),
            args.build_evidence.resolve() / "provenance.json",
            args.build_evidence.resolve() / "acceptance-summary.json",
        ]
        for service in TEST_IMAGES:
            source_files.extend(
                args.build_evidence.resolve() / f"{service}-{suffix}"
                for suffix in ("sbom.spdx.json", "vulnerabilities.sarif", "files.txt")
            )
        certification = read_json(args.test_certification.resolve())
        source_files.extend(
            args.test_certification.resolve().parent / record["file"]
            for record in certification["evidence"].values()
        )
        for source in source_files:
            destination = args.output.parent / source.name
            if source.resolve() != destination.resolve():
                if destination.exists():
                    raise PromotionError(f"refusing to overwrite promotion evidence: {destination.name}")
                shutil.copyfile(source, destination)
            package_files[destination.name] = sha256(destination)
        payload = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
        args.output.write_text(payload, encoding="utf-8", newline="\n")
        package_files[args.output.name] = hashlib.sha256(payload.encode("utf-8")).hexdigest()
        checksum_lines = "".join(f"{value}  {name}\n" for name, value in sorted(package_files.items()))
        sidecar_path.write_text(checksum_lines, encoding="ascii", newline="\n")
    except PromotionError as error:
        print(f"promotion blocked: {error}", file=sys.stderr)
        return 2
    print(f"promotion manifest checksummed: {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
