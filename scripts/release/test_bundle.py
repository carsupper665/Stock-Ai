import hashlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from bootstrap import verify
from export_bundle import Exporter


def digest(content):
    return "sha256:" + hashlib.sha256(content).hexdigest()


class PortableImagesTest(unittest.TestCase):
    def test_index_child_manifest_and_blob_bytes_keep_original_digests(self):
        config, layer = b'{"architecture":"amd64"}', b"compressed-layer-fixture"
        child = json.dumps({"schemaVersion": 2, "config": {"digest": digest(config)}, "layers": [{"digest": digest(layer)}]}).encode()
        index = json.dumps({"schemaVersion": 2, "manifests": [{"digest": digest(child)}, {"digest": digest(child)}]}).encode()
        values = {digest(value): value for value in (config, layer, child, index)}
        calls = []
        def fetch(request, **kwargs):
            key = request.full_url.rsplit("/", 1)[-1]
            calls.append(key)
            return io.BytesIO(values[key])
        with tempfile.TemporaryDirectory() as temporary, patch("export_bundle.urllib.request.urlopen", side_effect=fetch):
            exporter = Exporter(Path(temporary))
            exporter.image("127.0.0.1:5000/stock-ai/backend@" + digest(index))
            self.assertEqual(len(calls), 4)
            for key, content in values.items():
                value = key.split(":")[1]
                self.assertEqual((exporter.root / "blobs/sha256" / value[:2] / value / "data").read_bytes(), content)
            link = exporter.root / "repositories/stock-ai/backend/_manifests/revisions/sha256" / digest(index).split(":")[1] / "link"
            self.assertEqual(link.read_text(), digest(index))

    def test_corrupt_registry_blob_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary, patch("export_bundle.urllib.request.urlopen", return_value=io.BytesIO(b"corrupt")):
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                Exporter(Path(temporary)).blob("stock-ai/backend", digest(b"expected"))

    def test_archive_checksum_corruption_stops_install_before_docker(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "source.tar.gz").write_bytes(b"corrupt")
            (root / "SHA256SUMS").write_text(hashlib.sha256(b"expected").hexdigest() + "  source.tar.gz\n")
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                verify(root)

    def test_checksum_inventory_cannot_escape_package(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "SHA256SUMS").write_text("0" * 64 + "  ../outside\n")
            with self.assertRaisesRegex(ValueError, "inventory"):
                verify(root)


if __name__ == "__main__":
    unittest.main()
