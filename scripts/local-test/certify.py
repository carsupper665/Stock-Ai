#!/usr/bin/env python3
"""Run local Test acceptance against the digest-pinned Compose stack.

Evidence contains only statuses, sanitized identifiers and timings. Cookies,
tokens, response bodies, user identity, secret values and fixture claims stay in
memory and are never written to disk.
"""

import hashlib
import http.client
import http.cookiejar
import json
import os
import socket
import ssl
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
RUNTIME = ROOT / ".local-test"
EVIDENCE = ROOT / "deploy/evidence/local-test/certification"
APP = "https://app.localtest"
IDP = "https://idp.localtest:9443"
PROJECT = "stock-ai-t10-cert"
COMPOSE = ["docker", "compose", "--project-name", PROJECT, "--env-file", str(RUNTIME / "compose.env"),
           "-f", str(ROOT / "deploy/compose/compose.yaml"),
           "-f", str(ROOT / "deploy/compose/compose.test-edge.yaml"),
           "-f", str(ROOT / "deploy/compose/compose.local-test.yaml")]
RESULT_NAMES = (
    "secured_product_flow", "wrong_user", "expired_session", "idp_denial",
    "idp_restart", "internal_service_restart", "docker_restart_failure", "test_material_absence",
)
IMAGE_NAMES = ("backend", "agent-server", "llm-provider-server", "console", "oidc-gateway")


class LocalHTTPSConnection(http.client.HTTPSConnection):
    def connect(self):
        sock = socket.create_connection(("127.0.0.1", self.port), self.timeout, self.source_address)
        self.sock = self._context.wrap_socket(sock, server_hostname=self.host)


class LocalHTTPSHandler(urllib.request.HTTPSHandler):
    def https_open(self, request):
        return self.do_open(LocalHTTPSConnection, request, context=self._context)


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        return None


class StopAtLoginHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        if urllib.parse.urlparse(new_url).path == "/login":
            return None
        return super().redirect_request(request, response, code, message, headers, new_url)


class Browser:
    def __init__(self):
        self.context = ssl.create_default_context(cafile=str(RUNTIME / "certs/fullchain.pem"))
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), LocalHTTPSHandler(context=self.context),
            urllib.request.HTTPCookieProcessor(self.cookies),
        )

    def request(self, method, path, payload=None, origin=False, authorization=None, follow_redirects=True,
                stop_at_login=False):
        data = None if payload is None else json.dumps(payload).encode()
        headers = {"Accept": "application/json"}
        if data is not None:
            headers["Content-Type"] = "application/json"
        if origin:
            headers["Origin"] = APP
        if authorization:
            headers["Authorization"] = "Bearer " + authorization
        request = urllib.request.Request(APP + path, data=data, headers=headers, method=method)
        opener = self.opener
        if not follow_redirects or stop_at_login:
            opener = urllib.request.build_opener(
                urllib.request.ProxyHandler({}), LocalHTTPSHandler(context=self.context),
                urllib.request.HTTPCookieProcessor(self.cookies),
                NoRedirectHandler() if not follow_redirects else StopAtLoginHandler(),
            )
        try:
            response = opener.open(request, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        except urllib.error.URLError as error:
            raise RuntimeError(f"HTTPS request failed for app path {path}: {type(error.reason).__name__}") from error
        content_type = response.headers.get("Content-Type", "")
        body = response.read(2 * 1024 * 1024)
        parsed = None
        if "json" in content_type.lower():
            try:
                parsed = json.loads(body)
            except json.JSONDecodeError:
                pass
        return response.status, parsed, response.geturl()


def control(path, payload):
    context = ssl.create_default_context(cafile=str(RUNTIME / "certs/fullchain.pem"))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), LocalHTTPSHandler(context=context))
    request = urllib.request.Request(IDP + path, data=json.dumps(payload).encode(),
                                    headers={"Content-Type": "application/json"}, method="POST")
    try:
        response = opener.open(request, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    body = json.loads(response.read())
    return response.status, body


def login(browser, scenario="success", stop_at_login=False):
    time.sleep(1.2)
    status, result = control("/__fixture/next", {"scenario": scenario})
    assert status == 200 and result.get("accepted") == scenario, "fixture scenario setup failed"
    status, _, final_url = browser.request("GET", "/oauth2/start", stop_at_login=stop_at_login)
    final = urllib.parse.urlparse(final_url)
    acceptable_status = (200, 302, 303) if stop_at_login else (200,)
    assert status in acceptable_status and final.hostname == "app.localtest", (
        f"OIDC callback did not return to app (status={status}, host={final.hostname}, path={final.path})")


def current_user(browser):
    status, body, _ = browser.request("GET", "/oauth2/me")
    return status, body


def require_ok(actual, expected, description):
    if actual != expected:
        raise AssertionError(f"{description}: expected HTTP {expected}, received {actual}")


def run_flow(browser, prefix):
    login(browser)
    status, user = current_user(browser)
    require_ok(status, 200, "OIDC current user")
    assert isinstance(user, dict) and user, "current user projection missing"

    provider = {"id": prefix, "name": "Local Certification Provider", "type": "openai",
                "base_url": "http://fake-provider.localtest:18080/v1", "default_model": "fixture-model"}
    status, body, _ = browser.request("POST", "/llm/v1/providers", provider)
    require_ok(status, 201, "create fake provider")
    status, _, _ = browser.request("POST", f"/llm/v1/providers/{prefix}/tokens",
                                   {"name": "local-fixture", "token": "local-test-provider-token-only"})
    require_ok(status, 201, "create fake provider token")
    model_name = prefix + "-model"
    status, _, _ = browser.request("POST", "/llm/v1/model-catalog", {
        "model_name": model_name, "provider_id": prefix, "model": "fixture-model",
        "options": {}, "levels": {"high": {}}, "enabled": True,
    })
    require_ok(status, 201, "create local model mapping")
    status, account, _ = browser.request("POST", "/v1/accounts", {
        "user_name": prefix + "-account", "initial_balance": 10000,
    })
    require_ok(status, 201, "create account")
    assert isinstance(account, dict) and account.get("id") and account.get("token"), "account response incomplete"
    account_id = account["id"]
    account_token = account["token"]
    del account

    status, session, _ = browser.request("POST", "/agent/api/v1/sessions", {
        "name": prefix + "-session", "account_id": account_id, "model_name": model_name,
        "model_level": "high", "prompt": "Place one safe limit buy order for BTCUSDT at 100 USDT.",
        "max_loop": 3, "max_tool_call": 2,
    })
    if status != 201:
        code = session.get("error") or session.get("code") if isinstance(session, dict) else "unknown"
        raise AssertionError(f"create Agent session: expected HTTP 201, received {status}, code={code}")
    session_id = session.get("id") if isinstance(session, dict) else None
    assert session_id, "Agent session ID missing"
    status, _, _ = browser.request("POST", f"/agent/api/v1/sessions/{session_id}/start")
    assert status in (200, 202), f"start Agent session returned HTTP {status}"

    run_id = None
    run_status = ""
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        status, runs, _ = browser.request("GET", f"/agent/api/v1/sessions/{session_id}/runs")
        require_ok(status, 200, "read Agent runs")
        rows = runs.get("runs", []) if isinstance(runs, dict) else runs if isinstance(runs, list) else []
        if rows:
            run_id = rows[0].get("run_id", rows[0].get("id"))
            run_status = rows[0].get("status", "")
            if run_status in ("completed", "failed", "stopped"):
                break
        time.sleep(1)
    assert run_id is not None and run_status == "completed", f"Agent run did not complete (status={run_status or 'missing'})"
    status, orders, _ = browser.request("GET", "/v1/orders", authorization=account_token)
    require_ok(status, 200, "list orders created by Agent run")
    rows = orders.get("orders", []) if isinstance(orders, dict) else []
    assert rows, "secured Agent run completed without placing an order"
    del account_token
    status, _, _ = browser.request("POST", f"/agent/api/v1/sessions/{session_id}/stop")
    assert status in (200, 202, 204), f"stop Agent session returned HTTP {status}"
    status, _, _ = browser.request("POST", "/oauth2/logout", origin=True, follow_redirects=False)
    assert status in (200, 302, 303), f"same-origin logout returned HTTP {status}"
    status, _ = current_user(browser)
    assert status == 401, f"logout did not clear session (HTTP {status})"
    login(browser)
    status, _ = current_user(browser)
    require_ok(status, 200, "re-login after logout")
    return {"provider_created": True, "model_created": True, "account_created": True,
            "session_created": True, "run_status": run_status, "session_stopped": True,
            "logout_cleared_session": True, "relogin_succeeded": True}


def compose(*args, check=True):
    return subprocess.run(COMPOSE + list(args), cwd=ROOT, check=check, capture_output=True, text=True, timeout=120)


def wait_healthy(service, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = subprocess.run(["docker", "inspect", "--format", "{{.State.Health.Status}}", f"{PROJECT}-{service}-1"],
                                capture_output=True, text=True)
        if result.returncode == 0 and result.stdout.strip() == "healthy":
            return
        time.sleep(2)
    raise AssertionError(f"service health timeout: {service}")


def result_file(name, status, started, details=None, error=None):
    record = {"schema": "stock-ai.local-acceptance-result.v1", "name": name, "status": status,
              "started_at_utc": started, "finished_at_utc": datetime.now(timezone.utc).isoformat(),
              "details": details or {}}
    if error:
        record["error"] = error[:400]
    path = EVIDENCE / f"{name}.json"
    path.write_text(json.dumps(record, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {"file": path.name, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def run_case(name, callback):
    started = datetime.now(timezone.utc).isoformat()
    started_clock = time.monotonic()
    try:
        # Each case gets a clean in-memory Nginx rate-limit zone. The gateway
        # policy stays unchanged while cases remain independently repeatable.
        compose("restart", "nginx")
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", 443), timeout=1):
                    break
            except OSError:
                time.sleep(0.5)
        else:
            raise AssertionError("Test edge did not return after isolated Nginx restart")
        time.sleep(1)
        details = callback()
        evidence = result_file(name, "passed", started, {**details, "duration_seconds": round(time.monotonic() - started_clock, 3)})
        return evidence, "passed"
    except Exception as error:
        evidence = result_file(name, "failed", started, {"duration_seconds": round(time.monotonic() - started_clock, 3)},
                               f"{type(error).__name__}: {error}")
        return evidence, "failed"


def cert_main():
    global EVIDENCE
    EVIDENCE = EVIDENCE / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    EVIDENCE.mkdir(parents=True, exist_ok=False)
    evidence = {}
    results = {}
    run_prefix = "t10-" + EVIDENCE.name.replace("T", "").replace("Z", "")[-12:].lower()
    digests = json.loads((RUNTIME / "registry-references.json").read_text(encoding="utf-8"))
    compose("ps", "--format", "json")

    evidence["secured_product_flow"], results["secured_product_flow"] = run_case(
        "secured_product_flow", lambda: run_flow(Browser(), run_prefix))

    def wrong_user():
        browser = Browser()
        login(browser, "wrong-subject", stop_at_login=True)
        status, _ = current_user(browser)
        assert status == 401, f"wrong user obtained current session (HTTP {status})"
        status, _, _ = browser.request("GET", "/agent/api/v1/sessions", follow_redirects=False)
        assert status in (302, 401, 403), f"wrong user reached protected Agent API (HTTP {status})"
        return {"wrong_subject_rejected": True, "protected_api_rejected": True}

    evidence["wrong_user"], results["wrong_user"] = run_case("wrong_user", wrong_user)

    def expired_session():
        browser = Browser()
        login(browser, "short-token")
        time.sleep(3)
        status, _ = current_user(browser)
        assert status == 401, f"expired session remained valid (HTTP {status})"
        return {"short_lived_session_expired": True, "session_status": status}

    evidence["expired_session"], results["expired_session"] = run_case("expired_session", expired_session)

    def idp_denial():
        browser = Browser()
        status, result = control("/__fixture/next", {"scenario": "denial"})
        assert status == 200 and result.get("accepted") == "denial", "fixture denial setup failed"
        browser.request("GET", "/oauth2/start", follow_redirects=False)
        status, _ = current_user(browser)
        assert status == 401, f"IdP denial authenticated a user (HTTP {status})"
        return {"access_denied_rejected": True, "session_status": status}

    evidence["idp_denial"], results["idp_denial"] = run_case("idp_denial", idp_denial)

    def idp_restart():
        compose("restart", "local-fixtures")
        wait_healthy("local-fixtures")
        time.sleep(1.5)
        browser = Browser()
        login(browser)
        status, _ = current_user(browser)
        require_ok(status, 200, "OIDC login after fixture restart")
        return {"fixture_restarted": True, "health": "healthy", "login_after_restart": True}

    evidence["idp_restart"], results["idp_restart"] = run_case("idp_restart", idp_restart)

    def internal_restart():
        services = ("backend", "llm-provider-server", "agent-server", "console", "oidc-gateway")
        for service in services:
            compose("restart", service)
            wait_healthy(service)
        browser = Browser()
        login(browser)
        status, _ = current_user(browser)
        require_ok(status, 200, "login after internal service restarts")
        return {"restarted_services": list(services), "all_healthy": True, "login_after_restart": True}

    evidence["internal_service_restart"], results["internal_service_restart"] = run_case(
        "internal_service_restart", internal_restart)

    def docker_restart_failure():
        before = subprocess.run(["docker", "inspect", "--format", "{{.State.Health.Status}}", f"{PROJECT}-backend-1"],
                                capture_output=True, text=True, check=True).stdout.strip()
        failed = subprocess.run(["docker", "restart", f"{PROJECT}-missing-container"], capture_output=True, text=True)
        assert failed.returncode != 0, "invalid Docker restart target unexpectedly succeeded"
        after = subprocess.run(["docker", "inspect", "--format", "{{.State.Health.Status}}", f"{PROJECT}-backend-1"],
                               capture_output=True, text=True, check=True).stdout.strip()
        assert before == "healthy" and after == "healthy", "failed restart affected the valid stack"
        return {"invalid_restart_rejected": True, "backend_health_before": before, "backend_health_after": after,
                "stderr_recorded": False}

    evidence["docker_restart_failure"], results["docker_restart_failure"] = run_case(
        "docker_restart_failure", docker_restart_failure)

    def test_material_absence():
        build = RUNTIME / "build-evidence"
        private_material = [path.read_bytes().strip() for path in sorted((RUNTIME / "secrets").glob("*"))]
        private_material.extend((RUNTIME / "certs" / name).read_bytes().strip()
                                for name in ("fullchain.pem", "privkey.pem", "jwks-signing-key.pem"))
        private_material = [value for value in private_material if value]
        for service in IMAGE_NAMES:
            inventory = (build / f"{service}-files.txt").read_text(encoding="utf-8").lower()
            assert "stock-ai-test" not in inventory, f"Test material path in {service} image inventory"
            for command, description in (
                (["docker", "image", "inspect", digests[service], "--format", "{{json .Config}}"], "image config"),
                (["docker", "buildx", "imagetools", "inspect", "--raw", digests[service]], "registry manifest"),
            ):
                inspected = subprocess.run(command, capture_output=True, check=True).stdout
                lowered = inspected.lower()
                assert b"stock-ai-test_" not in lowered, f"Test secret path in {service} {description}"
                assert b"local-test-oidc-client-secret-only" not in lowered, f"fixture secret in {service} {description}"
                assert b"local-test-provider-token-only" not in lowered, f"provider fixture token in {service} {description}"
                assert not any(secret in inspected for secret in private_material), (
                    f"local Test secret or certificate material in {service} {description}")
            with tempfile.NamedTemporaryFile(dir=RUNTIME, suffix=".image.tar", delete=False) as temporary:
                archive = Path(temporary.name)
            try:
                subprocess.run(["docker", "image", "save", "--output", str(archive), digests[service]],
                               capture_output=True, check=True)
                with tarfile.open(archive, mode="r") as saved_image:
                    saved_manifest = json.loads(saved_image.extractfile("manifest.json").read())
                    layers = [layer for item in saved_manifest for layer in item.get("Layers", [])]
                    assert layers, f"No saved image layers for {service}"
                    for layer_name in layers:
                        with saved_image.extractfile(layer_name) as layer_stream:
                            with tarfile.open(fileobj=layer_stream, mode="r|") as layer:
                                for entry in layer:
                                    if not entry.isfile():
                                        continue
                                    content = layer.extractfile(entry).read()
                                    assert not any(secret in content for secret in private_material), (
                                        f"local Test secret or certificate material in {service} image layer")
                                    assert b"local-test-oidc-client-secret-only" not in content, (
                                        f"fixture secret in {service} image layer")
                                    assert b"local-test-provider-token-only" not in content, (
                                        f"provider fixture token in {service} image layer")
            finally:
                archive.unlink(missing_ok=True)
            metadata = (Path("/tmp") / f"stock-ai-t10-{service}-build.json").read_text(encoding="utf-8").lower()
            assert "local-test-oidc-client-secret-only" not in metadata, f"fixture secret in {service} build manifest"
        return {"images_checked": list(IMAGE_NAMES), "layers_config_registry_manifests_clear": True,
                "test_fixture_image_promoted": False}

    evidence["test_material_absence"], results["test_material_absence"] = run_case(
        "test_material_absence", test_material_absence)

    certification = {
        "schema": "stock-ai.test-certification.v1",
        "status": "passed" if all(value == "passed" for value in results.values()) else "failed",
        "revision": subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True,
                                    text=True, check=True).stdout.strip(),
        "configuration_schema_version": "stock-ai-compose-local.v1",
        "accepted_image_digests": {key: value.rsplit("@", 1)[1] for key, value in digests.items()},
        "results": results,
        "evidence": evidence,
        "completed_at_utc": datetime.now(timezone.utc).isoformat(),
    }
    path = EVIDENCE / "test-certification.json"
    path.write_text(json.dumps(certification, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    sums = {file.name: hashlib.sha256(file.read_bytes()).hexdigest() for file in sorted(EVIDENCE.glob("*.json"))}
    (EVIDENCE / "SHA256SUMS").write_text("".join(f"{value}  {name}\n" for name, value in sorted(sums.items())),
                                        encoding="ascii")
    print(f"Local Test certification {certification['status']} and checksummed: {path}")
    if certification["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    cert_main()
