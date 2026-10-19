#!/usr/bin/env python3
"""Read-only credential-loss inspection; never emit keys or database contents."""

import json
import subprocess
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from prepare import DEFAULT_MANIFEST, ROOT, approved_images
from scripts import linux_recovery as recovery


def inspect():
    helper = approved_images(DEFAULT_MANIFEST)[0]["nginx"]
    result = {}
    with tempfile.TemporaryDirectory(dir=ROOT / ".local-release") as temporary:
        base = Path(temporary)
        base.chmod(0o700)
        for service, volume, database, table in (
            ("backend", "backend_state", "backend.db", "accounts"),
            ("llm-provider-server", "llm_state", "llm-provider.db", "provider_tokens"),
        ):
            archive = base / (service + ".tar")
            command = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
                       "--security-opt", "no-new-privileges:true", "--user", "65532:65532",
                       "--mount", f"type=volume,src=stock-ai-local-release_{volume},dst=/data,readonly",
                       "--entrypoint", "/bin/tar", helper, "-czf", "-", "-C", "/data", "."]
            with archive.open("wb") as output:
                completed = subprocess.run(command, stdout=output, stderr=subprocess.PIPE, timeout=60)
            if completed.returncode:
                raise RuntimeError("Read-only volume inspection failed")
            destination = base / service
            destination.mkdir(mode=0o700)
            recovery.extract_archive(archive, destination)
            facts = recovery.inspect_sqlite(service, destination / database)
            result[service] = facts
            if table not in facts["table_counts"]:
                raise RuntimeError("Credential table missing")
        return {"schema": "stock-ai.release-material-loss.v1", "tested_at_utc": datetime.now(timezone.utc).isoformat(),
                "read_only_live_volume_inspection": True, "databases": result,
                "replacement_keys_safe": result["backend"]["table_counts"]["accounts"] == 0
                and result["llm-provider-server"]["table_counts"]["provider_tokens"] == 0}


if __name__ == "__main__":
    try:
        result = inspect()
    except Exception:
        raise SystemExit("Credential-loss inspection failed; private diagnostics suppressed")
    destination = ROOT / "deploy/evidence/local-release/t14/material-loss.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, sort_keys=True))
