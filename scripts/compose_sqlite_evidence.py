#!/usr/bin/env python3
"""Emit a secret-free logical fingerprint for stopped Compose SQLite files."""

from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
from pathlib import Path


def inspect_database(label: str, path: Path) -> dict[str, object]:
    if not path.is_file() or path.stat().st_size == 0:
        raise ValueError(f"{label}: database is missing or empty")
    connection = sqlite3.connect(f"file:{path.as_posix()}?mode=ro", uri=True)
    try:
        integrity = connection.execute("PRAGMA integrity_check").fetchone()[0]
        if integrity != "ok":
            raise ValueError(f"{label}: SQLite integrity_check failed")
        tables = [
            row[0]
            for row in connection.execute(
                "SELECT name FROM sqlite_master "
                "WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
            )
        ]
        counts = {
            table: connection.execute(
                f'SELECT COUNT(*) FROM "{table.replace(chr(34), chr(34) * 2)}"'
            ).fetchone()[0]
            for table in tables
        }
        schema = "\n".join(
            row[0] or ""
            for row in connection.execute(
                "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name"
            )
        )
    finally:
        connection.close()
    return {
        "integrity_check": "ok",
        "schema_sha256": hashlib.sha256(schema.encode("utf-8")).hexdigest(),
        "table_counts": counts,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--database",
        action="append",
        required=True,
        metavar="LABEL=PATH",
    )
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result: dict[str, object] = {}
    for item in args.database:
        label, separator, raw_path = item.partition("=")
        if not separator or not label or not raw_path:
            parser.error("--database must be LABEL=PATH")
        result[label] = inspect_database(label, Path(raw_path))
    args.output.write_text(
        json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    print(f"SQLite evidence valid: {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
