"""Private browser state and strictly verified loopback HTTPS for sign-off."""

import json
import subprocess
import time
from pathlib import Path
from urllib.parse import urljoin, urlparse

ORIGIN = "https://app.localrelease:8443"


class ProbeError(RuntimeError):
    pass


class Browser:
    def __init__(self, runtime, directory, port=8443):
        self.runtime, self.directory, self.port = runtime, directory, port
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        directory.chmod(0o700)
        self.jar = directory / "cookies"
        self.sequence = 0

    def request(self, path, method="GET", body=None, headers=None, retry=True):
        url = urljoin(ORIGIN, path)
        parsed = urlparse(url)
        if (parsed.scheme, parsed.hostname, parsed.port) != ("https", "app.localrelease", 8443):
            raise ProbeError("Browser navigation left its approved local origin")
        self.sequence += 1
        response = self.directory / f"body-{self.sequence}"
        header_file = self.directory / f"headers-{self.sequence}"
        command = ["curl", "--silent", "--show-error", "--noproxy", "*", "--max-time", "25",
                   "--cacert", str(self.runtime / "certs/fullchain.pem"),
                   "--connect-to", f"app.localrelease:8443:127.0.0.1:{self.port}",
                   "--cookie-jar", str(self.jar), "--cookie", str(self.jar),
                   "--dump-header", str(header_file), "--output", str(response),
                   "--write-out", "%{http_code}", "--request", method, "--config", "-"]
        values = {"Origin": ORIGIN, "Accept": "application/json", **(headers or {})}
        config = "url = " + json.dumps(url) + "\n"
        for key, value in values.items():
            if any(character in key + value for character in "\r\n"):
                raise ProbeError("Invalid request header")
            config += "header = " + json.dumps(key + ": " + value) + "\n"
        if body is not None:
            payload = self.directory / f"payload-{self.sequence}"
            payload.write_bytes(body if isinstance(body, bytes) else json.dumps(body).encode())
            payload.chmod(0o600)
            command += ["--header", "Content-Type: application/json", "--data-binary", "@" + str(payload)]
        for attempt in range(6 if retry else 1):
            result = subprocess.run(command, input=config, text=True, capture_output=True, timeout=30)
            if result.returncode:
                raise ProbeError("Verified HTTPS request failed")
            status = int(result.stdout)
            if status != 429 or not retry or attempt == 5:
                break
            time.sleep(12)
        parsed_headers = {}
        for line in header_file.read_text().splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                parsed_headers.setdefault(key.lower(), []).append(value.strip())
        return {"status": status, "headers": parsed_headers, "body": response.read_bytes(), "path": parsed.path}

    def follow(self, path="/"):
        chain = []
        for _ in range(10):
            response = self.request(path)
            chain.append({"path": response["path"], "status": response["status"]})
            if response["status"] not in (301, 302, 303, 307, 308):
                return response, chain
            locations = response["headers"].get("location", [])
            if len(locations) != 1:
                raise ProbeError("Ambiguous redirect")
            path = urljoin(urljoin(ORIGIN, path), locations[0])
        raise ProbeError("Redirect loop")

    def json(self, path, method="GET", body=None, expected=(200,)):
        response = self.request(path, method, body)
        if response["status"] not in expected:
            raise ProbeError("Public API returned an unexpected status")
        return json.loads(response["body"])


def fixture_control(path, payload):
    if path not in ("next", "outage"):
        raise ProbeError("Unapproved fixture control")
    result = subprocess.run([
        "docker", "exec", "stock-ai-local-release-nginx-1", "curl", "--silent", "--show-error",
        "--noproxy", "*", "--max-time", "10", "--cacert", "/run/release-tls/fullchain.pem",
        "--connect-to", "app.localrelease:8443:idp:8443", "--request", "POST",
        "--header", "Content-Type: application/json", "--data-raw", json.dumps(payload),
        "https://app.localrelease:8443/__fixture/" + path,
    ], capture_output=True, timeout=15)
    if result.returncode:
        raise ProbeError("Private fixture control failed")
    return json.loads(result.stdout)
