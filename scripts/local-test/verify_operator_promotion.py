#!/usr/bin/env python3
"""Run the local Release Operator against the generated promotion package."""

import hashlib
import json
import shutil
import tempfile
from datetime import datetime, timezone
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
PROMOTION = ROOT / "deploy/evidence/local-test/promotion/promotion.json"
OUTPUT = ROOT / "deploy/evidence/local-test/release-operator"
SERVICES = ("backend", "agent-server", "llm-provider-server", "middleware", "nginx")
import sys

sys.path.insert(0, str(ROOT / "scripts"))
from local_operations import Operator


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_record(directory: Path, name: str, manifest: dict, status: str, detail: dict) -> None:
    payload = {
        "schema": "stock-ai.local-release-operator-result.v1",
        "name": name,
        "status": status,
        "accepted_source_revision": manifest["git_revision"],
        "promotion_manifest_sha256": sha256(PROMOTION),
        "details": detail,
        "tested_at_utc": datetime.now(timezone.utc).isoformat(),
    }
    path = directory / f"{name}.json"
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def compose_text(images: dict[str, str], *, build=False) -> str:
    lines = ["services:"]
    for service in SERVICES:
        key = service.upper().replace("-", "_") + "_IMAGE"
        lines.extend((f"  {service}:", f"    image: ${{{key}}}"))
        if build and service == "backend":
            lines.extend(("    build:", "      context: ."))
    return "\n".join(lines) + "\n"


def run_preflight(directory: Path, label: str, images: dict[str, str], manifest_path: Path,
                  *, build=False) -> tuple[Operator, dict, int]:
    case = directory / label
    case.mkdir(parents=True)
    compose = case / "compose.yaml"
    compose.write_text(compose_text(images, build=build), encoding="utf-8")
    env = case / "compose.env"
    variables = {
        "STOCK_AI_INSTANCE": "stock-ai-local-release-check",
        "STOCK_AI_ENVIRONMENT": "Release",
        "STOCK_AI_SECRET_NAMESPACE": "stock-ai-release",
    }
    for service, image in images.items():
        variables[service.upper().replace("-", "_") + "_IMAGE"] = image
    env.write_text("".join(f"{key}={value}\n" for key, value in variables.items()), encoding="utf-8")
    env.chmod(0o600)
    operator = Operator(env, [compose], case / "operator-evidence", timeout=10)
    report, status = operator.preflight(manifest_path)
    return operator, report, status


def main() -> None:
    manifest = json.loads(PROMOTION.read_text(encoding="utf-8"))
    images = manifest["images"]
    if set(images) != set(SERVICES):
        raise SystemExit("generated promotion package does not contain the exact five Release images")
    if OUTPUT.exists():
        raise SystemExit(f"refusing to overwrite Release Operator evidence: {OUTPUT}")
    OUTPUT.mkdir(parents=True)

    with tempfile.TemporaryDirectory(prefix="stock-ai-operator-release-") as temporary:
        root = Path(temporary)
        exact_operator, exact_report, exact_code = run_preflight(root, "exact", images, PROMOTION)
        exact_config = exact_operator.compose_config()
        promoted, manifest_hash = exact_operator.promotion(PROMOTION, exact_config)
        if exact_code != 0 or not exact_report.get("passed") or promoted != images:
            raise SystemExit("Release Operator rejected the exact generated promotion package")
        write_record(OUTPUT, "release_operator_exact_digest", manifest, "passed", {
            "preflight_passed": True,
            "promotion_method_accepted": True,
            "manifest_sha256": manifest_hash,
            "services": sorted(promoted),
            "exact_image_digest_match": True,
        })

        substitution = dict(images)
        ref = substitution["backend"]
        digest = ref.rsplit("@", 1)[1]
        substitute_digest = "sha256:" + ("0" * 64 if digest != "sha256:" + "0" * 64 else "1" * 64)
        substitution["backend"] = ref.rsplit("@", 1)[0] + "@" + substitute_digest
        _, substituted_report, substituted_code = run_preflight(root, "substitution", substitution, PROMOTION)
        if substituted_code == 0 or not any("differ from the checksummed promotion manifest" in issue
                                            for issue in substituted_report["issues"]):
            raise SystemExit("Release Operator preflight accepted a substituted image digest")
        write_record(OUTPUT, "release_operator_substitution_rejected", manifest, "passed", {
            "preflight_rejected_substitution": True,
            "rejected_service": "backend",
        })

        tamper_dir = root / "tampered-package"
        shutil.copytree(PROMOTION.parent, tamper_dir)
        tampered_manifest = tamper_dir / "promotion.json"
        original_manifest_bytes = tampered_manifest.read_bytes()
        tampered_manifest_bytes = original_manifest_bytes + b" "
        if tampered_manifest_bytes == original_manifest_bytes:
            raise SystemExit("tamper fixture did not change manifest bytes")
        tampered_manifest.write_bytes(tampered_manifest_bytes)
        _, tamper_report, tamper_code = run_preflight(root, "tamper", images, tampered_manifest)
        if tamper_code == 0 or not any("SHA256SUMS" in issue for issue in tamper_report["issues"]):
            raise SystemExit("Release Operator accepted a tampered promotion package")
        write_record(OUTPUT, "release_operator_tamper_rejected", manifest, "passed", {
            "preflight_rejected_manifest_tamper": True,
            "sha256_sidecar_required": True,
        })

        _, build_report, build_code = run_preflight(root, "build-directive", images, PROMOTION, build=True)
        if build_code == 0 or not any("build directive" in issue for issue in build_report["issues"]):
            raise SystemExit("Release Operator preflight accepted a Compose build directive")
        write_record(OUTPUT, "release_operator_build_rejected", manifest, "passed", {
            "preflight_rejected_build_directive": True,
            "rejected_service": "backend",
        })

    sums = []
    for path in sorted(OUTPUT.glob("*.json")):
        sums.append(f"{sha256(path)}  {path.name}\n")
    (OUTPUT / "SHA256SUMS").write_text("".join(sums), encoding="ascii")

    packaged_results = PROMOTION.parent / "release-operator"
    if packaged_results.exists():
        raise SystemExit(f"refusing to overwrite package Release Operator evidence: {packaged_results}")
    packaged_results.mkdir()
    for result in sorted(OUTPUT.glob("*.json")):
        shutil.copyfile(result, packaged_results / result.name)
    checksum_file = PROMOTION.parent / "SHA256SUMS"
    package_entries = {}
    for line in checksum_file.read_text(encoding="ascii").splitlines():
        digest, name = line.split("  ", 1)
        package_entries[name] = digest
    for result in sorted(packaged_results.glob("*.json")):
        package_entries[result.relative_to(PROMOTION.parent).as_posix()] = sha256(result)
    checksum_file.write_text("".join(
        f"{digest}  {name}\n" for name, digest in sorted(package_entries.items())
    ), encoding="ascii")

    with tempfile.TemporaryDirectory(prefix="stock-ai-operator-postpackage-") as temporary:
        postpackage_operator, postpackage_report, postpackage_code = run_preflight(
            Path(temporary), "exact", images, PROMOTION)
        postpackage_config = postpackage_operator.compose_config()
        accepted, _ = postpackage_operator.promotion(PROMOTION, postpackage_config)
        if postpackage_code != 0 or not postpackage_report.get("passed") or accepted != images:
            raise SystemExit("Release Operator rejected the completed checksummed promotion package")
    print(f"Release Operator preflight/promotion checks passed against generated package: {OUTPUT}")


if __name__ == "__main__":
    main()
