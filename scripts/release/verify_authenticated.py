#!/usr/bin/env python3
"""Verify a real logged-in Console session through Backend, Agent and a tool-capable model."""
import argparse
import http.cookiejar
import json
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path


class AcceptanceError(RuntimeError):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


def session_request(base_url, cookie_file, ca_bundle=None):
    parsed = urllib.parse.urlsplit(base_url)
    if parsed.scheme != "https" or not parsed.netloc or parsed.username or parsed.password or parsed.path not in ("", "/") or parsed.query or parsed.fragment:
        raise AcceptanceError("base URL must be an HTTPS origin")
    origin = base_url.rstrip("/")
    cookies = http.cookiejar.MozillaCookieJar(str(cookie_file))
    cookies.load(ignore_discard=True)
    context = ssl.create_default_context()
    if ca_bundle:
        context.load_verify_locations(cafile=str(ca_bundle))
    opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPCookieProcessor(cookies),
                                        urllib.request.HTTPSHandler(context=context))

    def request(method, path, payload=None):
        headers = {"Accept": "application/json", "Origin": origin}
        data = None
        if payload is not None:
            data = json.dumps(payload).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(origin + path, data=data, headers=headers, method=method)
        try:
            with opener.open(req, timeout=30) as response:
                body = response.read(4 * 1024 * 1024 + 1)
                if len(body) > 4 * 1024 * 1024:
                    raise AcceptanceError("API response exceeds acceptance limit")
                return json.loads(body) if body else None
        except urllib.error.HTTPError as error:
            raise AcceptanceError(f"{method} {path}: HTTP {error.code}; authenticated acceptance failed") from None
        except (urllib.error.URLError, ValueError):
            raise AcceptanceError(f"{method} {path}: TLS, connection or JSON response failed") from None
    return request


def verify_tool_run(run):
    if run.get("status") != "completed" or run.get("error"):
        raise AcceptanceError("Agent Run did not complete successfully")
    if not isinstance(run.get("tool_call_count"), int) or run["tool_call_count"] < 1:
        raise AcceptanceError("completed Run has zero executed tools")
    progress = run.get("progress", [])
    if not isinstance(progress, list):
        raise AcceptanceError("Run has no tool execution evidence")
    calls = [entry for entry in progress if entry.get("type") == "tool_call"]
    if any(entry.get("tool_name") != "get_account" for entry in calls):
        raise AcceptanceError("acceptance Run called a tool outside the read-only probe")
    if not any(entry.get("tool_name") == "get_account" and entry.get("result", {}).get("ok") is True for entry in calls):
        raise AcceptanceError("Run has no successful get_account result")


def verify(request, model_name, timeout=180):
    report = {"schema": "stock-ai.authenticated-acceptance.v1", "status": "failed"}
    # These requests must precede mutations: a login redirect alone does not prove API access.
    for path in ("/oauth2/me", "/v1/me", "/v1/accounts", "/llm/v1/models", "/agent/api/v1/sessions"):
        request("GET", path)
    report["authenticated_apis"] = True
    account_id = session_id = None
    cleanup_errors = []
    try:
        name = "acceptance-" + uuid.uuid4().hex[:12]
        account_id = request("POST", "/v1/accounts", {"user_name": name, "initial_balance": 100})["id"]
        session_id = request("POST", "/agent/api/v1/sessions", {
            "name": name, "account_id": account_id, "model_name": model_name,
            "max_loop": 3, "max_tool_call": 1,
            "prompt": "Deployment acceptance: call get_account exactly once with empty arguments, then finish with the required finalization JSON. Do not call any other tool. If the tool is unavailable, report status failed.",
        })["id"]
        report["agent_backend_account_lookup"] = True
        path = "/agent/api/v1/sessions/" + urllib.parse.quote(session_id, safe="")
        request("POST", path + "/start")
        deadline = time.monotonic() + timeout
        while True:
            runs = request("GET", path + "/runs").get("runs", [])
            run = runs[0] if runs else {}
            if run.get("status") in ("completed", "failed", "stopped"):
                verify_tool_run(run)
                break
            if time.monotonic() >= deadline:
                raise AcceptanceError("Agent Run timed out")
            time.sleep(1)
        request("GET", "/oauth2/me")
        request("GET", "/v1/me")
        report.update(tool_execution_verified=True, session_still_authenticated=True)
    finally:
        if session_id:
            path = "/agent/api/v1/sessions/" + urllib.parse.quote(session_id, safe="")
            for method, target in (("POST", path + "/stop"), ("DELETE", path)):
                try:
                    request(method, target)
                except Exception:
                    cleanup_errors.append("session cleanup")
        if account_id:
            try:
                request("DELETE", "/v1/accounts/" + urllib.parse.quote(account_id, safe=""))
            except Exception:
                cleanup_errors.append("account cleanup")
        if cleanup_errors and sys.exc_info()[0] is None:
            raise AcceptanceError("acceptance probe cleanup failed; inspect acceptance-named Account/Session")
    report.update(status="passed", probe_resources_removed=True)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--cookie-file", required=True, type=Path, help="Private Netscape cookie jar from a real login")
    parser.add_argument("--ca-bundle", type=Path)
    parser.add_argument("--model-name", required=True, help="An enabled deployed model; the probe invokes it")
    parser.add_argument("--timeout", type=float, default=180)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    report = {"schema": "stock-ai.authenticated-acceptance.v1", "status": "failed"}
    try:
        report = verify(session_request(args.base_url, args.cookie_file, args.ca_bundle), args.model_name, args.timeout)
    except Exception as error:
        report["reason"] = str(error) if isinstance(error, AcceptanceError) else type(error).__name__
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
