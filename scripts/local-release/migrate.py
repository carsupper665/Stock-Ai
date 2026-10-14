#!/usr/bin/env python3
"""Consistent encrypted fixture migration with complete data/key rollback.

Run by the operator with Docker access. Nothing prints database rows, credentials,
HTTP response bodies or exception messages. Test containers are only paused,
never recreated: their original bind sources may no longer exist.
"""

import argparse
import base64
import hashlib
import json
import os
import sqlite3
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

from deploy import persistent, preflight, project_containers, snapshot, utc
from prepare import ROOT
from scripts import linux_recovery as recovery
from scripts.local_operations import Operator, WorkflowError, run

KEYS = ("llm_master_key", "backend_account_token_key")


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def database_facts(path):
    """Compare every row, every primary key and selected rows without disclosure."""
    facts = recovery.inspect_sqlite("migration", path)
    connection = sqlite3.connect(f"file:{path.as_posix()}?mode=ro", uri=True)
    try:
        if connection.execute("PRAGMA foreign_key_check").fetchall():
            raise WorkflowError("Database foreign key check failed")
        tables = {}
        for table in facts["table_counts"]:
            quoted = '"' + table.replace('"', '""') + '"'
            columns = connection.execute(f"PRAGMA table_info({quoted})").fetchall()
            primary = [index for index, column in sorted(enumerate(columns), key=lambda entry: entry[1][5])
                       if column[5]]
            rows = connection.execute(f"SELECT * FROM {quoted}").fetchall()
            def normalized(row):
                return [({"blob": base64.b64encode(value).decode()} if isinstance(value, bytes) else value)
                        for value in row]
            records = sorted(digest(normalized(row)) for row in rows)
            identifiers = sorted(digest(normalized([row[index] for index in primary])) for row in rows)
            tables[table] = {"count": len(rows), "all_records_sha256": digest(records),
                             "identifiers_sha256": digest(identifiers),
                             "representative_records_sha256": records[:3]}
        sequences = connection.execute("SELECT name FROM sqlite_master WHERE name='sqlite_sequence'").fetchone()
        sequence_rows = connection.execute("SELECT name, seq FROM sqlite_sequence ORDER BY name").fetchall() if sequences else []
        return {**facts, "foreign_key_check": "ok", "tables": tables, "sqlite_sequences_sha256": digest(sequence_rows)}
    finally:
        connection.close()


def store_facts(stage):
    history = stage / "agent-server/history"
    if not history.is_dir():
        raise WorkflowError("Agent History missing")
    history_files = recovery.inventory(history)
    return {"databases": {service: database_facts(stage / service / filename)
                           for service, filename in recovery.DATABASES.items()},
            "history_file_count": len(history_files), "history_sha256": digest(history_files)}


def copy_volumes(config, helper, destination):
    for service, volume in recovery.VOLUMES.items():
        archive = destination / (service + ".tar.gz")
        command = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--mount",
                   f"type=volume,src={config['volumes'][volume]['name']},dst=/data,readonly",
                   "--entrypoint", "/bin/tar", helper, "-czf", "-", "-C", "/data", "."]
        with archive.open("wb") as output:
            response = subprocess.run(command, stdout=output, stderr=subprocess.PIPE, timeout=120)
        if response.returncode:
            raise WorkflowError("Private volume export failed")
        directory = destination / service
        directory.mkdir(mode=0o700)
        recovery.extract_archive(archive, directory)
        archive.unlink()


def source_export(args, helper, directory):
    if args.source_project == "stock-ai-local-release":
        raise WorkflowError("Source and target projects must differ")
    ids = run(["docker", "ps", "-aq", "--filter",
               f"label=com.docker.compose.project={args.source_project}"]).stdout.split()
    entries = json.loads(run(["docker", "inspect", *ids]).stdout) if ids else []
    services = {entry["Config"]["Labels"]["com.docker.compose.service"]: entry for entry in entries}
    if any(service not in services for service in recovery.VOLUMES):
        raise WorkflowError("Source persistent services missing")
    config = {"volumes": {}}
    for service, volume in recovery.VOLUMES.items():
        mounts = [mount for mount in services[service]["Mounts"] if mount["Destination"] == "/data"]
        if len(mounts) != 1 or mounts[0]["Type"] != "volume":
            raise WorkflowError("Source data mount unsupported")
        config["volumes"][volume] = {"name": mounts[0]["Name"]}
    paused = []
    try:
        for entry in entries:
            if entry["State"]["Running"]:
                if entry["State"].get("Paused"):
                    raise WorkflowError("Source already paused")
                run(["docker", "pause", entry["Id"]])
                paused.append(entry["Id"])
        copy_volumes(config, helper, directory)
        secrets = directory / "keys"
        secrets.mkdir(mode=0o700)
        for suffix in KEYS:
            source = args.source_secrets / f"stock-ai-test_{suffix}"
            recovery.require_private_file(source, "Source encryption key")
            recovery._write_private(secrets / suffix, source.read_bytes())
    finally:
        for identifier in reversed(paused):
            run(["docker", "unpause", identifier])
    facts = store_facts(directory)
    if not facts["databases"]["backend"]["table_counts"].get("accounts") or not facts["databases"]["llm-provider-server"]["table_counts"].get("provider_tokens"):
        raise WorkflowError("Source lacks representative account/provider data")
    return facts


def decrypt_credentials(stage):
    """Authenticate every stored Provider ciphertext with its matching master key."""
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
    key = base64.b64decode((stage / "keys/llm_master_key").read_bytes().strip(), validate=True)
    connection = sqlite3.connect(f"file:{(stage / 'llm-provider-server/llm-provider.db').as_posix()}?mode=ro", uri=True)
    try:
        rows = connection.execute("SELECT id, provider_id, secret_encrypted FROM provider_tokens").fetchall()
        if not rows:
            raise WorkflowError("Encrypted fixture credentials missing")
        for token, provider, encrypted in rows:
            value = base64.b64decode(encrypted, validate=True)
            AESGCM(key).decrypt(value[:12], value[12:], (provider + "\0" + token).encode())
        return len(rows)
    finally:
        connection.close()


def verified_archive(backup, key_file, directory, expected_schema):
    recovery.verify_checksum(backup)
    plain = directory / "archive.tar.gz"
    recovery.decrypt_file(backup, plain, recovery.recovery_key(key_file))
    stage = directory / "contents"
    stage.mkdir(mode=0o700)
    recovery.extract_archive(plain, stage)
    manifest = json.loads((stage / "manifest.json").read_text())
    if manifest.get("schema") != expected_schema or manifest.get("files") != recovery.inventory(stage):
        raise WorkflowError("Authenticated archive inventory/schema differs")
    return stage


def replace_state(operator, config, stage, *, rollback=False):
    """Caller stops all target writers; replace whole volumes, including WAL/history."""
    helper = config["services"]["nginx"]["image"]
    for service, volume in recovery.VOLUMES.items():
        with tempfile.TemporaryDirectory(dir=stage.parent) as temporary:
            archive = Path(temporary) / "volume.tar.gz"
            recovery.build_archive(stage / service, archive)
            command = ["docker", "run", "--rm", "-i", "--network", "none", "--read-only", "--cap-drop", "ALL",
                       "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--mount",
                       f"type=volume,src={config['volumes'][volume]['name']},dst=/data",
                       "--entrypoint", "/bin/sh", helper, "-ec",
                       "find /data -mindepth 1 -maxdepth 1 -exec rm -rf {} +; tar -xzf - -C /data --no-same-owner"]
            with archive.open("rb") as source:
                result = subprocess.run(command, stdin=source, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120)
            if result.returncode:
                raise WorkflowError("Volume replacement failed")
    for suffix in KEYS:
        source = stage / "secrets" / f"stock-ai-release_{suffix}" if rollback else stage / "keys" / suffix
        destination = operator.env_file.parent / "secrets" / f"stock-ai-release_{suffix}"
        replace_key(destination, source.read_bytes())


def replace_key(destination, content):
    """All writers must be stopped. Preserve bind inode and read-only mode."""
    metadata = destination.lstat()
    if not stat.S_ISREG(metadata.st_mode):
        raise WorkflowError("Target encryption key must be a regular file")
    original_mode = stat.S_IMODE(metadata.st_mode)
    try:
        destination.chmod(0o600)
        with destination.open("r+b") as output:
            output.write(content)
            output.truncate()
            output.flush()
            os.fsync(output.fileno())
    finally:
        destination.chmod(original_mode)
    restored = destination.stat()
    if (restored.st_ino, restored.st_uid, restored.st_gid, stat.S_IMODE(restored.st_mode)) != (
            metadata.st_ino, metadata.st_uid, metadata.st_gid, original_mode):
        raise WorkflowError("Target encryption key metadata changed")


def stopped_facts(config, helper, parent):
    with tempfile.TemporaryDirectory(dir=parent) as temporary:
        stage = Path(temporary)
        stage.chmod(0o700)
        copy_volumes(config, helper, stage)
        return store_facts(stage)


def start(operator, config):
    operator.compose_run("up", "-d", "--force-recreate", "--no-build", "--pull", "never", "--wait", timeout=240)
    operator.wait_healthy(config)


def migrate(args):
    runtime = persistent(args.runtime)
    bundle = persistent(args.deployment_root)
    persistent(args.evidence_dir)
    files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, args.evidence_dir, 180)
    _, config, incumbents = preflight(operator, args.manifest)
    helper = config["services"]["nginx"]["image"]
    # Before mutation, authenticate the full predeployment snapshot (all target
    # keys and all three stores). It stays outside Git and survives this runner.
    backup = snapshot(operator, config, incumbents, runtime / "backups", runtime / "recovery.key")
    report = {"schema": "stock-ai.fixture-migration.v1", "status": "running",
              "started_at_utc": utc().isoformat(), "predeployment_backup_sha256": backup["sha256"]}
    operator.write_evidence("migration.json", report)
    with tempfile.TemporaryDirectory(prefix="fixture-migration-", dir=runtime) as temporary:
        base = Path(temporary)
        base.chmod(0o700)
        source = base / "source"
        source.mkdir(mode=0o700)
        source_facts = source_export(args, helper, source)
        report["source"] = source_facts
        report["source_credentials_authenticated"] = decrypt_credentials(source)
        manifest = {"schema": "stock-ai.fixture-transfer.v1", "consistency": "all-source-containers-paused",
                    "files": recovery.inventory(source)}
        recovery.write_json_private(source / "manifest.json", manifest)
        plain = base / "source.tar.gz"
        recovery.build_archive(source, plain)
        transfer = runtime / "backups" / ("fixture-transfer-" + utc().strftime("%Y%m%dT%H%M%S%fZ") + ".sai-backup")
        recovery.encrypt_file(plain, transfer, recovery.recovery_key(runtime / "recovery.key"))
        report["transfer_sha256"] = recovery.write_checksum(transfer)
        for name in ("verified-source", "verified-target"):
            (base / name).mkdir(mode=0o700)
        source = verified_archive(transfer, runtime / "recovery.key", base / "verified-source", manifest["schema"])
        previous = verified_archive(Path(backup["backup_file"]), runtime / "recovery.key", base / "verified-target", backup["schema"])
        previous_facts = store_facts(previous)
        report["predeployment"] = previous_facts
        mutated = False
        try:
            operator.compose_run("stop", "--timeout", "30")
            mutated = True
            replace_state(operator, config, source)
            if stopped_facts(config, helper, base) != source_facts:
                raise WorkflowError("Migrated records differ")
            # Deliberate failed checkpoint: restore the independently encrypted
            # predeployment snapshot, compare before any writer starts, then
            # prove healthy services. No source restart is performed.
            replace_state(operator, config, previous, rollback=True)
            if stopped_facts(config, helper, base) != previous_facts:
                raise WorkflowError("Rollback lost predeployment data")
            if any((runtime / "secrets" / f"stock-ai-release_{suffix}").read_bytes() !=
                   (previous / "secrets" / f"stock-ai-release_{suffix}").read_bytes() for suffix in KEYS):
                raise WorkflowError("Rollback encryption keys differ")
            start(operator, config)
            report["failed_checkpoint_rollback"] = {"checkpoint": "post-transfer-before-start", "status": "passed",
                                                   "all_records_and_history_match": True, "matching_keys_restored": True,
                                                   "all_services_healthy": True}
            if not args.rehearsal_only:
                operator.compose_run("stop", "--timeout", "30")
                replace_state(operator, config, source)
                migrated = stopped_facts(config, helper, base)
                if migrated != source_facts:
                    raise WorkflowError("Migration comparison failed")
                report["target"] = migrated
                start(operator, config)
                if args.business_smoke:
                    from business import probe
                    report["business"] = {}
                    probe(runtime, report["business"], config)
            report["status"] = "rehearsal-passed" if args.rehearsal_only else (
                "passed" if args.business_smoke else "migration-passed-business-pending")
            report["matching_master_and_account_keys_transferred"] = not args.rehearsal_only
            report["business_acceptance"] = "passed" if args.business_smoke else "pending-separate-smoke-gate"
            mutated = False
        except Exception:
            if mutated:
                operator.compose_run("stop", "--timeout", "30")
                replace_state(operator, config, previous, rollback=True)
                if stopped_facts(config, helper, base) != previous_facts:
                    raise WorkflowError("Automatic rollback comparison failed")
                start(operator, config)
                report["automatic_rollback"] = "passed"
            report["status"] = "failed"
            operator.write_evidence("migration.json", report)
            raise
    report["completed_at_utc"] = utc().isoformat()
    operator.write_evidence("migration.json", report)
    return report


if __name__ == "__main__":
    from prepare import DEFAULT_MANIFEST
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--deployment-root", required=True, type=Path)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--source-project", default="stock-ai-t10-cert")
    parser.add_argument("--source-secrets", required=True, type=Path)
    parser.add_argument("--manifest", default=DEFAULT_MANIFEST, type=Path)
    parser.add_argument("--rehearsal-only", action="store_true")
    parser.add_argument("--business-smoke", action="store_true",
                        help="Run mandatory market/Provider/Agent public business probe within rollback boundary")
    args = parser.parse_args()
    if args.rehearsal_only and args.business_smoke:
        parser.error("Rehearsal-only cannot run the migrated business probe")
    try:
        result = migrate(args)
        print(json.dumps({"status": result["status"], "business_acceptance": result["business_acceptance"]}))
    except Exception:
        sys.exit("Fixture migration failed; private diagnostics suppressed. Check sanitized evidence; no completion claimed.")
