#!/usr/bin/env python3
"""Check that the five services' VERSION files are mutually compatible.

Each ``<service>/VERSION`` holds the service's own version on the first line and one
``<service> <version>`` dependency per following line. Versions look like 5.0.1-b20r5;
only X.Y.Z is compared. A dependency must match exactly, except that the frontend is
backward compatible and accepts any dependency at or below the version it declares.
Image builds run this first and stop on any incompatibility.
"""

import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SERVICES = ("backend", "agent_server", "llm_provider_server", "middleware", "frontend")
BACKWARD_COMPATIBLE = {"frontend"}
VERSION = re.compile(r"^(\d+)\.(\d+)\.(\d+)-b\d+r\d+$")


class VersionError(Exception):
    pass


def release(version):
    match = VERSION.match(version)
    if not match:
        raise VersionError(f"{version!r} is not X.Y.Z-bNrM")
    return tuple(int(part) for part in match.groups())


def read(root):
    """Return {service: (version, {dependency: version})}."""
    services = {}
    for service in SERVICES:
        path = root / service / "VERSION"
        lines = [line.strip() for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
        if not lines:
            raise VersionError(f"{path} is empty")
        requires = {}
        for line in lines[1:]:
            parts = line.split()
            if len(parts) != 2:
                raise VersionError(f"{path}: {line!r} is not '<service> <version>'")
            requires[parts[0]] = parts[1]
        services[service] = (lines[0], requires)
    return services


def check(root=ROOT):
    """Return the services' versions, or raise VersionError listing every problem."""
    services = read(root)
    problems = []
    for service, (version, requires) in services.items():
        try:
            release(version)
        except VersionError as error:
            problems.append(f"{service}: {error}")
        for dependency, wanted in requires.items():
            if dependency not in services:
                problems.append(f"{service} requires unknown service {dependency}")
                continue
            actual = services[dependency][0]
            try:
                compatible = release(actual) <= release(wanted) if service in BACKWARD_COMPATIBLE else release(actual) == release(wanted)
            except VersionError as error:
                problems.append(f"{service} requires {dependency}: {error}")
                continue
            if not compatible:
                rule = "at most " if service in BACKWARD_COMPATIBLE else ""
                problems.append(f"{service} requires {dependency} {rule}{wanted}, found {actual}")
    if problems:
        raise VersionError("\n".join(problems))
    return {service: version for service, (version, _) in services.items()}


def main():
    try:
        versions = check()
    except (OSError, VersionError) as error:
        print(f"Incompatible service versions:\n{error}", file=sys.stderr)
        return 1
    for service, version in versions.items():
        print(f"{service} {version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
