"""Install a scanned immutable auxiliary image, retaining full profile rollback."""
import argparse
import hashlib
import json
from pathlib import Path

from accept import sync_bundle, restore_profile
from deploy import project_containers, rollback_metadata, snapshot
from migrate import replace_key, verified_archive
from prepare import AUXILIARY, DEFAULT_MANIFEST
from scripts.local_operations import Operator, WorkflowError, run
import tempfile


def update(runtime, scans):
    report = json.loads((scans / "summary.json").read_text())
    if report["status"] != "passed" or report["images"]["nginx"]["high_critical_count"]:
        raise WorkflowError("No passing Nginx vulnerability scan")
    candidate = report["images"]["nginx"]["image"]
    raw = (scans / "nginx.json").read_bytes()
    if hashlib.sha256(raw).hexdigest() != report["images"]["nginx"]["sha256"]:
        raise WorkflowError("Candidate scan hash differs")
    bundle = runtime / "deployment"
    files = [bundle / "deploy/compose" / f for f in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, runtime / "evidence/t16", 180)
    before = operator.compose_config()
    backup = snapshot(operator, before, project_containers(), runtime / "backups", runtime / "recovery.key")
    operator.write_evidence("pre-nginx-update-backup.json", backup)
    source = AUXILIARY.read_bytes()
    with tempfile.TemporaryDirectory(dir=runtime) as temporary:
        base = Path(temporary)
        previous = verified_archive(Path(backup["backup_file"]), runtime / "recovery.key", base,
                                    "stock-ai.complete-release-backup.v1")
        try:
            approval = json.loads(source)
            approval["nginx"] = {"status": "tested", "image": candidate,
                "base_image": "nginx@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94",
                "dockerfile": "deploy/local-release/Dockerfile.nginx",
                "evidence": "deploy/evidence/local-release/t16/scans/summary.json",
                "scan_sha256": hashlib.sha256(raw).hexdigest(),
                "patched_packages": {"libexpat": "2.8.5-r0", "pcre2": "10.49-r0", "tiff": "4.7.2-r0"}}
            AUXILIARY.write_text(json.dumps(approval, indent=2) + "\n")
            sync_bundle(bundle)
            values = {"NGINX_IMAGE": candidate, "AUXILIARY_IMAGES_SHA256": hashlib.sha256(AUXILIARY.read_bytes()).hexdigest()}
            lines = operator.env_file.read_text().splitlines()
            replace_key(operator.env_file, ("\n".join(
                key + "=" + values[key] if (key := line.partition("=")[0]) in values else line
                for line in lines) + "\n").encode())
            operator = Operator(operator.env_file, files, operator.evidence_dir, 180)
            operator.compose_run("up", "-d", "--no-deps", "--force-recreate", "--no-build", "--pull", "never", "nginx")
            operator.wait_healthy(operator.compose_config())
            from verify_ingress import verify
            ingress = verify(runtime)
            operator.write_evidence("nginx-update.json", {"status": "passed", "before": before["services"]["nginx"]["image"],
                "after": candidate, "preupdate_complete_backup_sha256": backup["sha256"],
                "all_seven_healthy": True, "verified_ingress": ingress, "high_critical_count": 0})
            operator.write_evidence("rollback.json", rollback_metadata(operator, operator.compose_config(), DEFAULT_MANIFEST))
            print("Nginx updated: seven healthy services, verified login, zero High/Critical", flush=True)
        except Exception:
            AUXILIARY.write_bytes(source)
            restore_profile(operator, before, previous, base)
            raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--scans", type=Path, required=True)
    args = parser.parse_args()
    try:
        update(args.runtime, args.scans)
    except Exception as error:
        raise SystemExit("Nginx update failed closed: " + type(error).__name__)
