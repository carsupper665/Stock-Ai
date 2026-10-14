#!/usr/bin/env python3
"""Use real curl redirects to prove the isolated local Release public origin."""

import argparse
import json
import subprocess
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urljoin, urlparse

from prepare import HOST, ROOT


class IngressError(RuntimeError):
    def __init__(self, message, observations=None):
        super().__init__(message)
        self.observations = observations or []


def curl_with_retry(command, stage, observations, *, json_output=False):
    # The unchanged 5/minute login zone refills one slot every 12 seconds.
    # Bound each request to five retries; retain only sanitized HTTP status.
    for attempt in range(6):
        response = subprocess.run(command, check=True, capture_output=True, text=True)
        status = int(json.loads(response.stdout)["http_code"]) if json_output else int(response.stdout)
        observations.append({"stage": stage, "attempt": attempt + 1, "http_status": status})
        if status not in (429, 503):
            return response
        if attempt == 5:
            raise IngressError("Bounded HTTP 429/503 retry exhausted", observations)
        time.sleep(12)


def verify():
    origin = f"https://{HOST}"
    cert = ROOT / ".local-release/certs/fullchain.pem"
    chain = []
    observations = []
    with tempfile.TemporaryDirectory(prefix="curl-release-", dir=ROOT / ".local-release") as temporary:
        directory = Path(temporary)
        jar = directory / "cookies.txt"
        headers = directory / "headers.txt"
        common = ["curl", "--silent", "--show-error", "--noproxy", "*", "--cacert", str(cert),
                  "--resolve", f"{HOST}:127.0.0.1", "--cookie-jar", str(jar), "--cookie", str(jar),
                  "--output", "/dev/null", "--max-time", "20"]
        url = origin + "/"
        for _ in range(10):
            target = urlparse(url)
            if (target.scheme, target.hostname, target.port) != ("https", "app.localrelease", 8443):
                raise IngressError("Login chain left the Release origin", observations)
            response = curl_with_retry(common + ["--dump-header", str(headers), "--write-out", "%{http_code}", url],
                                       target.path, observations)
            status = int(response.stdout)
            chain.append({"host": target.hostname, "port": target.port, "path": target.path, "status": status})
            locations = [line.partition(":")[2].strip() for line in headers.read_text().splitlines()
                         if line.lower().startswith("location:")]
            if status == 200:
                break
            if status not in (301, 302, 303, 307, 308) or len(locations) != 1:
                raise IngressError("Login chain did not complete", observations)
            url = urljoin(url, locations[0])
        else:
            raise IngressError("Login redirect loop", observations)
        jar.unlink(missing_ok=True)
        response = curl_with_retry(common + ["--location", "--max-redirs", "10", "--write-out", "%{json}", origin+"/"],
                                   "curl --location", observations, json_output=True)
        details = json.loads(response.stdout)
        final = urlparse(details["url_effective"])
        if details["http_code"] != 200 or (final.hostname, final.port) != ("app.localrelease", 8443):
            raise IngressError("curl --location did not finish on Release", observations)
        if not any(step["path"] == "/oauth2/callback" for step in chain):
            raise IngressError("Signed-in callback was not exercised", observations)
        response = curl_with_retry(common + ["--write-out", "%{http_code}", origin+"/v1/accounts"],
                                   "authenticated API", observations)
        authenticated_api_status = int(response.stdout)
        if authenticated_api_status != 200:
            raise IngressError("Cookie-backed OIDC API authorization failed", observations)
        jar.unlink(missing_ok=True)
        response = curl_with_retry(common + ["--write-out", "%{http_code}", origin+"/v1/accounts"],
                                   "anonymous API", observations)
        anonymous_api_status = int(response.stdout)
        if anonymous_api_status != 401:
            raise IngressError("Anonymous API access was not rejected", observations)
    return {"schema": "stock-ai.local-release-ingress.v1", "status": "passed",
            "tested_at_utc": datetime.now(timezone.utc).isoformat(), "client": "curl with certificate verification",
            "redirect_chain": chain, "curl_location_final_status": details["http_code"],
            "curl_location_redirects": details["num_redirects"], "all_redirects_release_port": True,
            "test_origin_visited": False, "authenticated_api_status": authenticated_api_status,
            "anonymous_api_status": anonymous_api_status, "http_observations": observations,
            "rate_retry_policy": {"statuses": [429, 503], "wait_seconds": 12, "maximum_retries_per_request": 5}}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    try:
        evidence = verify()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        evidence = {"schema": "stock-ai.local-release-ingress.v1", "status": "failed",
                    "tested_at_utc": datetime.now(timezone.utc).isoformat(),
                    "http_observations": getattr(error, "observations", []),
                    "reason": "Release ingress did not pass; sensitive diagnostics suppressed"}
        args.evidence.parent.mkdir(parents=True, exist_ok=True)
        args.evidence.write_text(json.dumps(evidence, indent=2)+"\n")
        print(json.dumps(evidence, sort_keys=True))
        raise SystemExit("Release ingress verification failed; credentials and redirect queries suppressed.")
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence, sort_keys=True))
