#!/usr/bin/env python3
"""Export only validated, explicitly named sanitized evidence; never runtime material."""

import argparse
import hashlib
import json
from pathlib import Path

from prepare import DEFAULT_MANIFEST, ROOT, approved_images
from validate_acceptance import FILES, validate, validate_records


def export(source, destination, manifest=DEFAULT_MANIFEST):
    if source.is_symlink() or destination.is_symlink():
        raise ValueError("Evidence directories must not be symlinks")
    # Validate all records before copying a single byte into the public tree.
    content = {}
    for name in FILES:
        path = source / name
        if path.is_symlink() or not path.is_file():
            raise ValueError("Regular sanitized evidence file required")
        content[name] = path.read_bytes()
    images, checksum = approved_images(manifest)
    validate_records({name: json.loads(value) for name, value in content.items()}, images, checksum)
    destination.mkdir(parents=True, exist_ok=True)
    for name, value in content.items():
        target = destination / name
        if target.is_symlink():
            raise ValueError("Evidence destination must not be symlink")
        target.write_bytes(value)
        target.chmod(0o644)
    (destination / "SHA256SUMS").write_text("".join(
        hashlib.sha256(content[name]).hexdigest() + "  " + name + "\n" for name in FILES))
    return validate(destination, manifest)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--destination", type=Path, default=ROOT / "deploy/evidence/local-release/t15")
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    args = parser.parse_args()
    try:
        print(json.dumps(export(args.source, args.destination, args.manifest), sort_keys=True))
    except Exception:
        raise SystemExit("T15 export rejected; no private material copied or completion claimed")
