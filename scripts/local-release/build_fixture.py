#!/usr/bin/env python3
"""Test/build/pin only the auxiliary local fixture; never rebuild product images."""

import argparse
import hashlib
import json
import subprocess
import tempfile
from pathlib import Path

from prepare import AUXILIARY, ROOT

REQUIRED_TESTS = {"TestLocalReleaseMarketRoutesAndProvenance", "TestReleaseProviderMarketOrderCompletionSequence",
                  "TestProviderHasNoBackendAdapterOrRejectionRoutes"}


def build(builder):
    source = ROOT / "scripts/local-test"
    tested = subprocess.run(["go", "test", "-json", "./..."], cwd=source, check=True,
                            capture_output=True, text=True, timeout=120)
    events = [json.loads(line) for line in tested.stdout.splitlines() if line.startswith("{")]
    passed = sorted(event["Test"] for event in events if event.get("Action") == "pass" and event.get("Test"))
    if not REQUIRED_TESTS.issubset(passed):
        raise RuntimeError("Mandatory market fixture tests did not pass")
    hashes = {"scripts/local-test/" + name: hashlib.sha256((source / name).read_bytes()).hexdigest()
              for name in ("fixture.go", "Dockerfile", "go.mod")}
    tag = "127.0.0.1:5000/stock-ai/local-identity-fixture:t15-" + hashes["scripts/local-test/fixture.go"][:12]
    with tempfile.TemporaryDirectory() as temporary:
        metadata = Path(temporary) / "build.json"
        subprocess.run(["docker", "buildx", "build", "--builder", builder, "--platform", "linux/amd64",
                        "--file", str(source / "Dockerfile"), "--tag", tag, "--network", "none",
                        "--pull=false", "--provenance=mode=max", "--metadata-file", str(metadata),
                        "--push", str(source)], check=True, capture_output=True, timeout=1800)
        result = json.loads(metadata.read_text())
    digest = result.get("containerimage.digest", "")
    if len(digest) != 71 or not digest.startswith("sha256:"):
        raise RuntimeError("Auxiliary build did not produce immutable digest")
    image = tag.split(":t15-")[0] + "@" + digest
    subprocess.run(["docker", "pull", image], check=True, capture_output=True, timeout=120)
    approval = json.loads(AUXILIARY.read_text())
    approval["idp"] = {"status": "tested", "image": image, "source_sha256": hashes,
                       "evidence": "scripts/local-test/fixture_test.go", "test_command": "go test -json ./... (scripts/local-test)",
                       "passed_tests": passed, "test_output_sha256": hashlib.sha256(tested.stdout.encode()).hexdigest(),
                       "scope": "Auxiliary only: strict local TLS Binance market, OIDC, fake Provider; five product digests unchanged"}
    AUXILIARY.write_text(json.dumps(approval, indent=2) + "\n")
    return {"image": image, "passed_tests": passed, "product_images_rebuilt": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--builder", default="stock-ai-t10")
    args = parser.parse_args()
    try:
        print(json.dumps(build(args.builder), sort_keys=True))
    except Exception:
        raise SystemExit("Auxiliary build failed; no approval fabricated; private diagnostics suppressed")
