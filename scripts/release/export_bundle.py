#!/usr/bin/env python3
"""Export exact accepted manifests and blobs without changing their digests."""
import argparse
import gzip
import hashlib
import json
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/local-release"))
from prepare import DEFAULT_MANIFEST, approved_images
from validate_signoff import validate

SHA = re.compile(r"sha256:[0-9a-f]{64}\Z")
MEDIA = ", ".join(("application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json",
                   "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json"))


def command(*args):
    return subprocess.check_output(args, cwd=ROOT)


def link(path, digest):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(digest)


class Exporter:
    def __init__(self, root):
        self.root = root / "docker/registry/v2"
        self.saved = set()

    def blob(self, repository, digest, manifest=False):
        if not SHA.fullmatch(digest):
            raise ValueError("Invalid image descriptor")
        value = digest.split(":")[1]
        destination = self.root / "blobs/sha256" / value[:2] / value / "data"
        if digest not in self.saved:
            kind = "manifests" if manifest else "blobs"
            request = urllib.request.Request(f"http://127.0.0.1:5000/v2/{repository}/{kind}/{digest}", headers={"Accept": MEDIA})
            destination.parent.mkdir(parents=True, exist_ok=True)
            checksum = hashlib.sha256()
            with urllib.request.urlopen(request, timeout=120) as source, destination.open("wb") as output:
                while chunk := source.read(1024 * 1024):
                    checksum.update(chunk)
                    output.write(chunk)
            if checksum.hexdigest() != value:
                raise ValueError("Registry content digest mismatch")
            self.saved.add(digest)
        repository_root = self.root / "repositories" / repository
        if manifest:
            link(repository_root / "_manifests/revisions/sha256" / value / "link", digest)
            metadata = json.loads(destination.read_bytes())
            for child in metadata.get("manifests", []):
                self.blob(repository, child["digest"], manifest=True)
            for child in [metadata.get("config"), *metadata.get("layers", [])]:
                if child:
                    self.blob(repository, child["digest"])
        else:
            link(repository_root / "_layers/sha256" / value / "link", digest)

    def image(self, reference):
        prefix = "127.0.0.1:5000/"
        if not reference.startswith(prefix):
            raise ValueError("Unexpected registry")
        repository, digest = reference[len(prefix):].split("@", 1)
        if not re.fullmatch(r"stock-ai/[a-z0-9-]+", repository):
            raise ValueError("Unexpected image repository")
        self.blob(repository, digest, manifest=True)
        tag = self.root / "repositories" / repository / "_manifests/tags/release"
        link(tag / "current/link", digest)
        link(tag / "index/sha256" / digest.split(":")[1] / "link", digest)


def build(output, tag):
    if command("git", "status", "--porcelain").strip():
        raise ValueError("Commit reviewed release files before exporting")
    validate(ROOT / "deploy/evidence/local-release/t16")
    images, promotion = approved_images(DEFAULT_MANIFEST)
    output.mkdir(parents=True, exist_ok=True)
    target = output / ("Stock-Ai-" + tag + ".tar.gz")
    if target.exists():
        raise ValueError("Output already exists")
    with tempfile.TemporaryDirectory(dir=output) as temporary:
        base = Path(temporary)
        package = base / ("Stock-Ai-" + tag)
        package.mkdir()
        registry = base / "registry"
        exporter = Exporter(registry)
        for service, reference in sorted(images.items()):
            print("Exporting exact image: " + service, flush=True)
            exporter.image(reference)
        with tarfile.open(package / "registry-data.tar.gz", "w:gz") as archive:
            archive.add(registry / "docker", arcname="docker")
        registry_tag = "stock-ai/release-registry:" + tag
        command("docker", "tag", "registry:2", registry_tag)
        registry_id = command("docker", "image", "inspect", registry_tag, "--format", "{{.Id}}").decode().strip()
        process = subprocess.Popen(["docker", "image", "save", registry_tag], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        with gzip.open(package / "registry-image.tar.gz", "wb", compresslevel=6) as output_file:
            shutil.copyfileobj(process.stdout, output_file)
        process.stdout.close()
        if process.wait() != 0:
            raise RuntimeError("Registry helper export failed")
        command("git", "archive", "--format=tar.gz", "--prefix=source/", "--output=" + str(package / "source.tar.gz"), "HEAD")
        shutil.copyfile(ROOT / "scripts/release/bootstrap.py", package / "bootstrap.py")
        shutil.copyfile(ROOT / "docs/deployment/remote-windows-release.md", package / "README.zh-TW.md")
        manifest = {"schema": "stock-ai.portable-release.v1", "tag": tag,
                    "packaging_revision": command("git", "rev-parse", "HEAD").decode().strip(),
                    "accepted_revision": "376295f0de3159f0b6a73712170311f394989a59",
                    "scope": "WSL/Linux local fixtures; remote Windows uses a Linux container engine",
                    "platform": "linux/amd64", "images": images, "promotion_manifest_sha256": promotion,
                    "registry_image": registry_tag, "registry_image_id": registry_id,
                    "contains_runtime_secrets_or_user_data": False, "exported_blob_count": len(exporter.saved)}
        (package / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        (package / "SHA256SUMS").write_text("\n".join(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.name
            for path in sorted(package.iterdir()) if path.is_file()) + "\n")
        with tarfile.open(target, "w:gz", compresslevel=1) as archive:
            archive.add(package, arcname=package.name)
        shutil.copyfile(package / "manifest.json", output / "release-manifest.json")
        (output / "SHA256SUMS").write_text(hashlib.sha256(target.read_bytes()).hexdigest() + "  " + target.name + "\n" +
            hashlib.sha256((output / "release-manifest.json").read_bytes()).hexdigest() + "  release-manifest.json\n")
        print(json.dumps({"bundle": str(target), "bytes": target.stat().st_size, "images": len(images), "blobs": len(exporter.saved)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--tag", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9._-]*", args.tag):
        parser.error("Invalid release tag")
    build(args.output.resolve(), args.tag)
