#!/usr/bin/env python3
"""Install approved auxiliary market fixture, migrate and certify; rollback all changes."""

import argparse
import hashlib
import json
import os
import shutil
import sys
import tempfile
from pathlib import Path

from deploy import persistent, project_containers, rollback_metadata, snapshot, utc
from migrate import migrate, replace_key, replace_state, start, stopped_facts, store_facts, verified_archive
from prepare import AUXILIARY, DEFAULT_MANIFEST, ROOT, approved_images, fixture_config, material
from scripts.local_operations import Operator, WorkflowError, run


def sync_bundle(bundle):
    # A persistent reviewed bundle must also retain the source bytes which the
    # auxiliary approval hashes, plus the certified promotion package.
    shutil.copytree(ROOT / "deploy", bundle / "deploy", dirs_exist_ok=True)
    shutil.copytree(ROOT / "scripts", bundle / "scripts", dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__"))


def restore_profile(operator, config, previous, base):
    """Restore full old profile, not just images or the four-service T13 schema."""
    operator.compose_run("down", "--timeout", "30")  # named volumes retained
    replace_state(operator, config, previous, rollback=True)
    runtime = operator.env_file.parent
    for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem", "config.json"):
        replace_key(runtime / "certs" / name, (previous / "certs" / name).read_bytes())
    for source in (previous / "secrets").iterdir():
        replace_key(runtime / "secrets" / source.name, source.read_bytes())
    replace_key(operator.env_file, (previous / "compose.env").read_bytes())
    bundle = operator.compose_files[0].parents[2]
    shutil.copytree(previous / "deploy", bundle / "deploy", dirs_exist_ok=True)
    if operator.env_file.read_bytes() != (previous / "compose.env").read_bytes() or any(
            (runtime / "certs" / name).read_bytes() != (previous / "certs" / name).read_bytes()
            for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem", "config.json")):
        raise WorkflowError("Full profile rollback material differs")
    if any((runtime / "secrets" / name.name).read_bytes() != name.read_bytes()
           for name in (previous / "secrets").iterdir()):
        raise WorkflowError("Full profile rollback keys differ")
    if stopped_facts(config, config["services"]["nginx"]["image"], base) != store_facts(previous):
        raise WorkflowError("Full profile rollback lost data/history")
    restored = Operator(operator.env_file, operator.compose_files, operator.evidence_dir, 180)
    start(restored, config)
    return {"status": "passed", "all_data_history_keys_tls_fixture_env_compose_restored": True,
            "all_original_services_healthy": True, "named_volumes_preserved": True}


def install_profile(operator, runtime, bundle, images, args):
    # An internal network property change requires target network recreation.
    operator.compose_run("down", "--timeout", "30")
    sync_bundle(bundle)
    with tempfile.TemporaryDirectory(prefix="t15-tls-", dir=runtime) as tls_temporary:
        tls = Path(tls_temporary)
        tls.chmod(0o700)
        material._ensure_tls_pair(tls / "fullchain.pem", tls / "privkey.pem", os.getgid(),
                                  ("app.localrelease", "api.binance.com"))
        for name in ("fullchain.pem", "privkey.pem"):
            replace_key(runtime / "certs" / name, (tls / name).read_bytes())
    replace_key(runtime / "certs/config.json", (json.dumps(fixture_config(), indent=2) + "\n").encode())
    lines = operator.env_file.read_text().splitlines()
    updates = {"LOCAL_RELEASE_FIXTURE_IMAGE": images["idp"],
               "AUXILIARY_IMAGES_SHA256": hashlib.sha256(AUXILIARY.read_bytes()).hexdigest()}
    if not set(updates).issubset({line.partition("=")[0] for line in lines}):
        raise WorkflowError("Incumbent auxiliary image fingerprints missing")
    lines = [line.partition("=")[0] + "=" + updates[line.partition("=")[0]]
             if line.partition("=")[0] in updates else line for line in lines]
    replace_key(operator.env_file, ("\n".join(lines) + "\n").encode())
    updated = Operator(runtime / "compose.env", operator.compose_files, args.evidence_dir, 180)
    from lint import lint
    lint(updated.env_file, args.manifest, operator.compose_files)
    new_config = updated.compose_config()
    start(updated, new_config)
    return updated, new_config


def accept(args):
    runtime, bundle = persistent(args.runtime), persistent(args.deployment_root)
    if bundle != runtime / "deployment":
        raise WorkflowError("Deployment bundle must be the dedicated runtime/deployment directory")
    persistent(args.evidence_dir)
    files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    original = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
    config = original.compose_config()
    # New fixture source/approval cannot pass the old T14 preflight. Validate
    # exact unchanged product promotion independently before backing up the
    # incumbent old auxiliary/config/CA profile.
    images, manifest_sha = approved_images(args.manifest)
    for service in ("backend", "agent-server", "llm-provider-server", "console", "oidc-gateway"):
        if config["services"][service]["image"] != images[service]:
            raise WorkflowError("Incumbent product digest differs from certification")
    incumbents = project_containers()
    if len(incumbents) != 7 or any(not entry["State"]["Running"] or
                                 entry["State"].get("Health", {}).get("Status") != "healthy" for entry in incumbents):
        raise WorkflowError("Complete incumbent healthy profile required")
    if any(entry["Config"]["Image"] != config["services"][entry["Config"]["Labels"]["com.docker.compose.service"]]["image"]
           for entry in incumbents):
        raise WorkflowError("Incumbent actual digest differs from persistent configuration")
    issues = []
    original._check_bind_sources(config, issues)
    if issues:
        raise WorkflowError("Incumbent persistence missing")
    for service in config["services"].values():
        for mount in service.get("volumes", []):
            if mount.get("type") == "bind":
                persistent(Path(mount["source"]))
    for image in images.values():
        run(["docker", "image", "inspect", image])
    backup = snapshot(original, config, incumbents, runtime / "backups", runtime / "recovery.key")
    original.write_evidence("predeployment-backup.json", backup)
    report = {"schema": "stock-ai.t15-acceptance.v1", "status": "running", "started_at_utc": utc().isoformat(),
              "predeployment_backup_sha256": backup["sha256"], "promotion_manifest_sha256": manifest_sha,
              "product_images": {name: images[name] for name in images if name not in ("idp", "nginx")},
              "auxiliary_image": images["idp"], "product_images_rebuilt": False}
    original.write_evidence("acceptance.json", report)
    with tempfile.TemporaryDirectory(prefix="t15-profile-", dir=runtime) as temporary:
        base = Path(temporary)
        base.chmod(0o700)
        previous = verified_archive(Path(backup["backup_file"]), runtime / "recovery.key", base, backup["schema"])
        try:
            report["checkpoint"] = "install-local-market-profile"
            updated, new_config = install_profile(original, runtime, bundle, images, args)
            # Exercise the full rollback at a deliberate failed checkpoint,
            # including old CA, auxiliary digest, network, keys and records.
            report["checkpoint"] = "deliberate-full-profile-rollback"
            report["full_profile_failed_checkpoint"] = restore_profile(updated, config, previous, base)
            restored = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
            report["checkpoint"] = "reinstall-local-market-profile"
            updated, new_config = install_profile(restored, runtime, bundle, images, args)
            # Complete data/key rollback rehearsal and public mandatory business
            # gate occur inside migrate's own rollback boundary too.
            report["checkpoint"] = "migration-and-mandatory-business-flow"
            migration = migrate(args)
            if migration.get("status") != "passed" or migration.get("business_acceptance") != "passed":
                raise WorkflowError("Mandatory migration/business gate incomplete")
            from verify_runtime import verify
            from verify_ingress import verify as ingress
            report["checkpoint"] = "runtime-and-ingress"
            updated.write_evidence("runtime.json", verify(args.source_project))
            updated.write_evidence("ingress.json", ingress(runtime))
            metadata = rollback_metadata(updated, new_config, args.manifest)
            updated.write_evidence("rollback.json", metadata)
            report["fingerprints"] = metadata
            report["migration"] = "passed"
            report["business"] = migration["business"]
            report["failed_checkpoint_rollback"] = migration["failed_checkpoint_rollback"]
            report["status"] = "passed"
            report["completed_at_utc"] = utc().isoformat()
            # Complete new snapshot binds the approved fixture, fresh CA,
            # migrated keys/data and current fingerprints for T16 recovery.
            report["checkpoint"] = "accepted-complete-profile-snapshot"
            final_backup = snapshot(updated, new_config, project_containers(), runtime / "backups", runtime / "recovery.key")
            updated.write_evidence("accepted-backup.json", final_backup)
            report["accepted_snapshot_sha256"] = final_backup["sha256"]
            report["checkpoint"] = "complete"
        except BaseException:
            report["status"] = "failed"
            try:
                # Read current Compose env after any partial replacement, rather
                # than restoring with a stale interpolation environment.
                current = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
                report["full_profile_rollback"] = restore_profile(current, config, previous, base)
            except BaseException:
                report["full_profile_rollback"] = {"status": "failed", "operator_recovery_required": True}
            original.write_evidence("acceptance.json", report)
            raise
    original.write_evidence("acceptance.json", report)
    return report


def rollback(args):
    runtime, bundle = persistent(args.runtime), persistent(args.deployment_root)
    if bundle != runtime / "deployment":
        raise WorkflowError("Rollback bundle must be runtime/deployment")
    files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
    with tempfile.TemporaryDirectory(prefix="t15-rollback-", dir=runtime) as temporary:
        base = Path(temporary)
        base.chmod(0o700)
        previous = verified_archive(args.restore_backup, runtime / "recovery.key", base, "stock-ai.complete-release-backup.v1")
        old_files = [previous / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
        saved = Operator(previous / "compose.env", old_files, args.evidence_dir, 180)
        result = restore_profile(operator, saved.compose_config(), previous, base)
        operator.write_evidence("manual-full-profile-rollback.json", result)
        return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--deployment-root", required=True, type=Path)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--source-secrets", type=Path)
    parser.add_argument("--restore-backup", type=Path, help="Authenticate and restore a complete-profile encrypted snapshot")
    parser.add_argument("--source-project", default="stock-ai-t10-cert")
    parser.add_argument("--manifest", default=DEFAULT_MANIFEST, type=Path)
    args = parser.parse_args()
    if not args.restore_backup and not args.source_secrets:
        parser.error("--source-secrets is required for acceptance")
    args.rehearsal_only, args.business_smoke = False, True
    try:
        result = rollback(args) if args.restore_backup else accept(args)
        print(json.dumps({"status": result["status"], "product_images_rebuilt": False,
                          "fixture_market_read": result.get("business", {}).get("fixture_market_read")}))
    except BaseException:
        sys.exit("T15 acceptance failed; private diagnostics suppressed. Check acceptance.json for full-profile rollback status.")
