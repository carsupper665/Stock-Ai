#!/usr/bin/env python3
"""Start the prebuilt local Release only after its isolation lint passes."""

import argparse
import json
from pathlib import Path

from lint import FILES, lint
from prepare import DEFAULT_MANIFEST, ROOT, material, persistent_runtime
from scripts.local_operations import Operator, WorkflowError


def start(env_file, manifest, files=FILES):
    persistent_runtime(env_file.parent)
    result = lint(env_file, manifest, files)
    operator = Operator(env_file, files, env_file.parent / "evidence", 120)
    operator.compose_run("up", "-d", "--no-build", "--wait")
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, required=True)
    parser.add_argument("--deployment-root", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    args = parser.parse_args()
    try:
        deployment = persistent_runtime(args.deployment_root)
        files = [deployment / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
        print(json.dumps(start(args.env_file, args.manifest, files), sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError, WorkflowError, material.PrepareError):
        raise SystemExit("Release startup failed closed; run the Release lint and inspect private operator diagnostics.")
