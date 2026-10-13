import sqlite3
import tempfile
import unittest
from pathlib import Path

from scripts.compose_sqlite_evidence import inspect_database


class ComposeSQLiteEvidenceTest(unittest.TestCase):
    def test_integrity_schema_and_logical_counts_are_recorded_without_rows(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.db"
            connection = sqlite3.connect(path)
            connection.execute("CREATE TABLE records (id INTEGER PRIMARY KEY, secret TEXT)")
            connection.execute("INSERT INTO records(secret) VALUES ('must-not-leak')")
            connection.commit()
            connection.close()

            evidence = inspect_database("fixture", path)

        self.assertEqual(evidence["integrity_check"], "ok")
        self.assertEqual(evidence["table_counts"], {"records": 1})
        self.assertEqual(len(evidence["schema_sha256"]), 64)
        self.assertNotIn("must-not-leak", str(evidence))

    def test_missing_database_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "missing or empty"):
                inspect_database("fixture", Path(directory) / "missing.db")


if __name__ == "__main__":
    unittest.main()
