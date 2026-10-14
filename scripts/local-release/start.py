#!/usr/bin/env python3
"""Start the prebuilt local Release only after its isolation lint passes."""

import argparse
import json
from pathlib import Path

from lint import FILES, lint
from prepare import DEFAULT_MANIFEST, ROOT, material
from scripts.local_operations import Operator, WorkflowError


def start(env_file, manifest):
    result = lint(env_file, manifest)
    operator = Operator(env_file, FILES, env_file.parent / "evidence", 120)
    operator.compose_run("up", "-d", "--no-build", "--wait")
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=ROOT / ".local-release/compose.env")
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    args = parser.parse_args()
    try:
        print(json.dumps(start(args.env_file, args.manifest), sort_keys=True))
    except (OSError, ValueError, KeyError, TypeError, WorkflowError, material.PrepareError):
        raise SystemExit("Release startup failed closed; run the Release lint and inspect private operator diagnostics.")
