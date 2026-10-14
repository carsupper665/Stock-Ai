#!/usr/bin/env python3
"""Capture sanitized actual Release digest, ingress and Test-isolation evidence."""

import argparse
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from lint import SERVICES
from prepare import DEFAULT_MANIFEST, INSTANCE, ROOT, approved_images


def docker(*args):
    response = subprocess.run(["docker", *args], check=True, capture_output=True, text=True, timeout=30)
    return response.stdout


def containers(project):
    ids = docker("ps", "-q", "--filter", f"label=com.docker.compose.project={project}").split()
    if not ids:
        raise RuntimeError("Expected Compose project is not running")
    return json.loads(docker("inspect", *ids))


def verify(test_project):
    release = containers(INSTANCE)
    test = containers(test_project)
    images, checksum = approved_images(DEFAULT_MANIFEST)
    records = {}
    published = []
    for entry in release:
        service = entry["Config"]["Labels"]["com.docker.compose.service"]
        health = entry["State"].get("Health", {}).get("Status")
        if not entry["State"]["Running"] or (service != "nginx" and health != "healthy"):
            raise RuntimeError("Release service is not healthy")
        if entry["Config"]["Image"] != images[service]:
            raise RuntimeError("Running image differs from approval")
        if service == "backend" and "ALLOW_STATIC_USER_TOKEN=false" not in entry["Config"]["Env"]:
            raise RuntimeError("Running Backend static authentication is not disabled")
        for target, bindings in entry["NetworkSettings"]["Ports"].items():
            for binding in bindings or []:
                published.append({"service": service, "host_ip": binding["HostIp"],
                                  "host_port": binding["HostPort"], "target": target})
        records[service] = {"image": entry["Config"]["Image"], "running": True, "health": health or "not-configured"}
    if set(records) != SERVICES or published != [{"service": "nginx", "host_ip": "127.0.0.1", "host_port": "8443", "target": "8443/tcp"}]:
        raise RuntimeError("Running ingress or service set differs from contract")
    isolation = {}
    for kind in ("networks", "volumes", "binds"):
        def sources(stack):
            if kind == "networks":
                return {name for entry in stack for name in entry["NetworkSettings"]["Networks"]}
            return {mount.get("Name", mount["Source"]) for entry in stack for mount in entry["Mounts"]
                    if mount["Type"] == ("volume" if kind == "volumes" else "bind")}
        release_sources, test_sources = sources(release), sources(test)
        if not release_sources or release_sources & test_sources:
            raise RuntimeError("Test and Release share runtime resources")
        isolation[kind] = {"disjoint_from_test": True}
        if kind != "binds":
            isolation[kind]["release_names"] = sorted(release_sources)
    return {"schema": "stock-ai.local-release-runtime.v1", "status": "passed",
            "tested_at_utc": datetime.now(timezone.utc).isoformat(), "project": INSTANCE,
            "test_project": test_project, "promotion_manifest_sha256": checksum,
            "services": records, "published_ingress": published, "isolation": isolation,
            "backend_static_user_token": "disabled"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--test-project", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    try:
        evidence = verify(args.test_project)
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError):
        raise SystemExit("Runtime isolation verification failed; private container configuration suppressed.")
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(evidence, indent=2)+"\n")
    print(json.dumps(evidence, sort_keys=True))
