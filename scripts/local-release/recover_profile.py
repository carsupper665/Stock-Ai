#!/usr/bin/env python3
"""Restore a complete local Release profile after authenticating its backup.

The selected archive must be one created by deploy.snapshot. A fresh snapshot
of the incumbent is retained before stopping it. Application keys, databases,
History, TLS/JWKS, environment and deployment configuration restore together.
"""
import argparse
import json
import tempfile
from pathlib import Path

from accept import restore_profile
from deploy import persistent, project_containers, snapshot
from migrate import verified_archive
from scripts.local_operations import DIGEST_RE, Operator, WorkflowError, run


def recover(runtime, backup, evidence):
    runtime = persistent(runtime)
    bundle = runtime / "deployment"
    files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, evidence, 180)
    current = operator.compose_config()
    with tempfile.TemporaryDirectory(prefix="recover-", dir=runtime) as temporary:
        base = Path(temporary)
        stage = verified_archive(backup, runtime / "recovery.key", base, "stock-ai.complete-release-backup.v1")
        manifest = json.loads((stage / "manifest.json").read_text())
        if set(manifest["images"]) != set(current["services"]):
            raise WorkflowError("Backup service set differs")
        for reference in manifest["images"].values():
            if not DIGEST_RE.fullmatch(reference):
                raise WorkflowError("Backup contains a mutable image")
            run(["docker", "image", "inspect", reference])
        fresh = snapshot(operator, current, project_containers(), runtime / "backups", runtime / "recovery.key")
        operator.write_evidence("pre-recovery-backup.json", fresh)
        try:
            result = restore_profile(operator, current, stage, base)
        except Exception:
            undo = base / "undo"
            undo.mkdir(mode=0o700)
            incumbent = verified_archive(Path(fresh["backup_file"]), runtime / "recovery.key", undo,
                                         "stock-ai.complete-release-backup.v1")
            restore_profile(operator, current, incumbent, undo)
            operator.write_evidence("failed-recovery-rollback.json", {"status": "passed", "incumbent_restored": True})
            raise
        result.update({"source_backup_sha256": __import__("hashlib").sha256(backup.read_bytes()).hexdigest(),
                       "pre_recovery_backup_sha256": fresh["sha256"]})
        operator.write_evidence("recovery.json", result)
        print("Complete Release profile restored; all seven services healthy")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--backup", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        recover(args.runtime, args.backup, args.evidence_dir)
    except Exception as error:
        raise SystemExit("Complete-profile recovery failed: " + type(error).__name__ + "; retain backups and inspect private operator evidence")
