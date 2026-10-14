#!/usr/bin/env python3
"""Pin the five current BuildKit output digests for the local Test Compose stack."""

import json
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / ".local-test"
SERVICES = ("backend", "agent-server", "llm-provider-server", "console", "oidc-gateway")


def main():
    references = {}
    for service in SERVICES:
        metadata_path = Path("/tmp") / f"stock-ai-t10-{service}-build.json"
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        digest = metadata.get("containerimage.digest", "")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise SystemExit(f"BuildKit metadata has no immutable digest for {service}")
        references[service] = f"127.0.0.1:5000/stock-ai/{service}@{digest}"
    destination = RUNTIME / "registry-references.json"
    destination.write_text(json.dumps(references, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    destination.chmod(0o600)
    print(f"Pinned local Test images from BuildKit metadata: {destination}")


if __name__ == "__main__":
    main()
