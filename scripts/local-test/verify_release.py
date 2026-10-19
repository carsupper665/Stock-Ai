"""Small fail-closed digest gate shared by local Release and certification."""

import hashlib
import re


RELEASE_SERVICES = ("backend", "agent-server", "llm-provider-server", "middleware", "nginx")
IMMUTABLE = re.compile(r"^[^\s@]+@sha256:[0-9a-f]{64}$")


class ReleaseVerificationError(ValueError):
    pass


def verify_manifest_bytes(payload, expected_sha256):
    actual = hashlib.sha256(payload).hexdigest()
    if actual != expected_sha256:
        raise ReleaseVerificationError("promotion manifest checksum mismatch")


def verify_release_configuration(manifest, compose):
    if manifest.get("schema") != "stock-ai.promotion-manifest.v1" or manifest.get("status") != "promoted":
        raise ReleaseVerificationError("unsupported or unpromoted manifest")
    if manifest.get("environment") != "Test":
        raise ReleaseVerificationError("promotion source must be Test")
    images = manifest.get("images")
    tested = manifest.get("test_stack_images")
    artifacts = manifest.get("artifacts")
    services = compose.get("services") if isinstance(compose, dict) else None
    if not isinstance(images, dict) or not isinstance(tested, dict) or not isinstance(artifacts, dict):
        raise ReleaseVerificationError("promotion image maps are missing")
    if not isinstance(services, dict):
        raise ReleaseVerificationError("Release compose services are missing")
    if set(services) != set(RELEASE_SERVICES):
        raise ReleaseVerificationError("Release compose service set differs from the promotion contract")
    for service in RELEASE_SERVICES:
        entry = services.get(service)
        if not isinstance(entry, dict) or entry.get("build"):
            raise ReleaseVerificationError(f"Release service has a build directive: {service}")
        reference = entry.get("image")
        if not isinstance(reference, str) or not IMMUTABLE.fullmatch(reference):
            raise ReleaseVerificationError(f"Release image is not digest pinned: {service}")
        if reference != images.get(service) or reference != tested.get(service):
            raise ReleaseVerificationError(f"Release image differs from Test promotion: {service}")
        expected_digest = artifacts.get(service, {}).get("build_digest")
        if expected_digest != reference.rsplit("@", 1)[1]:
            raise ReleaseVerificationError(f"Release image differs from tested build digest: {service}")
    return {service: services[service]["image"] for service in RELEASE_SERVICES}
