#!/usr/bin/env python3
"""Cookie-authenticated public Release business probe; secret-free evidence.

The immutable Backend keeps its hardcoded Binance URL. Docker DNS routes it
to the local TLS fixture, with certificate verification enabled. Private route
counters and public market responses are mandatory provenance checks.
"""

import json
import subprocess
import tempfile
import time
from pathlib import Path

from scripts.local_operations import WorkflowError

ORIGIN = "https://app.localrelease:8443"


class Browser:
    def __init__(self, runtime, directory):
        self.runtime, self.directory = runtime, directory
        self.jar = directory / "cookies"
        self.observations = []

    def request(self, method, path, payload=None, authorization=None, follow=False, expected=(200,)):
        response = self.directory / "response"
        command = ["curl", "--silent", "--show-error", "--noproxy", "*", "--cacert",
                   str(self.runtime / "certs/fullchain.pem"), "--resolve", "app.localrelease:8443:127.0.0.1",
                   "--cookie-jar", str(self.jar), "--cookie", str(self.jar), "--max-time", "25",
                   "--request", method, "--output", str(response), "--write-out", "%{http_code}",
                   "--header", "Accept: application/json", "--header", "Origin: " + ORIGIN]
        if follow:
            command += ["--location", "--max-redirs", "10"]
        config = ""
        if authorization:
            # Credentials are supplied on stdin, never process arguments.
            if any(character in authorization for character in '\r\n"\\'):
                raise WorkflowError("Invalid account token shape")
            config = 'header = "Authorization: Bearer ' + authorization + '"\n'
            command += ["--config", "-"]
        if payload is not None:
            payload_file = self.directory / "payload"
            payload_file.write_text(json.dumps(payload))
            payload_file.chmod(0o600)
            command += ["--header", "Content-Type: application/json", "--data-binary", "@" + str(payload_file)]
        command.append(ORIGIN + path)
        for attempt in range(6):
            completed = subprocess.run(command, input=config, text=True, capture_output=True, timeout=30)
            if completed.returncode:
                raise WorkflowError("Certificate-verified public request failed")
            status = int(completed.stdout)
            self.observations.append({"method": method, "status": status, "attempt": attempt + 1})
            if status not in (429, 503) or attempt == 5:
                break
            time.sleep(12)
        if status not in expected:
            raise WorkflowError("Public business API checkpoint failed")
        try:
            return json.loads(response.read_bytes())
        except json.JSONDecodeError:
            return None


def fixture_observation(config, path):
    if path != "market":
        raise WorkflowError("Unapproved fixture observation")
    command = ["docker", "run", "--rm", "--network", "stock-ai-local-release_application", "--read-only",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "65532:65532",
               "--entrypoint", "/bin/sh", config["services"]["nginx"]["image"], "-ec",
               "wget -qO- http://fake-provider.localrelease:18080/__fixture/" + path]
    result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=30)
    value = json.loads(result.stdout)
    if value.get("external_calls") != 0:
        raise WorkflowError("Local market provenance missing")
    return value


def market_counters(config):
    value = fixture_observation(config, "market")
    if value.get("fixture") != "stock-ai-local-binance-v1":
        raise WorkflowError("Local market fixture identity differs")
    return value


def market_boundary(config):
    network = json.loads(subprocess.run(["docker", "network", "inspect", "stock-ai-local-release_application"],
                                       check=True, capture_output=True, text=True, timeout=30).stdout)[0]
    if not network["Internal"]:
        raise WorkflowError("Market application network permits external routing")
    ids = subprocess.run(["docker", "ps", "-q", "--filter", "label=com.docker.compose.project=stock-ai-local-release"],
                         check=True, capture_output=True, text=True, timeout=30).stdout.split()
    entries = json.loads(subprocess.run(["docker", "inspect", *ids], check=True, capture_output=True, text=True, timeout=30).stdout)
    services = {entry["Config"]["Labels"]["com.docker.compose.service"]: entry for entry in entries}
    if any(services[name]["Config"]["Image"] != config["services"][name]["image"] for name in config["services"]):
        raise WorkflowError("Running digest mismatch")
    fixture = services["idp"]["NetworkSettings"]["Networks"][network["Name"]]
    if "api.binance.com" not in fixture.get("Aliases", []):
        raise WorkflowError("Binance does not resolve to local fixture")
    backend_env = services["backend"]["Config"]["Env"]
    if "SSL_CERT_FILE=/run/release-tls/fullchain.pem" not in backend_env or any(
            entry.split("=", 1)[0].lower() in ("https_proxy", "http_proxy", "all_proxy") and entry.partition("=")[2]
            for entry in backend_env):
        raise WorkflowError("Backend TLS/proxy boundary differs")
    return {"application_network_internal": True, "binance_dns_alias": "local-idp-fixture",
            "backend_certificate_verification": "existing SSL_CERT_FILE; no bypass", "all_running_digests_match": True}


def probe(runtime, report, config):
    report["market_boundary"] = market_boundary(config)
    with tempfile.TemporaryDirectory(prefix="business-", dir=runtime) as temporary:
        directory = Path(temporary)
        directory.chmod(0o700)
        browser = Browser(runtime, directory)
        report["http_observations"] = browser.observations
        browser.request("GET", "/", follow=True)
        if not browser.request("GET", "/oauth2/me"):
            raise WorkflowError("Fixture session not established")
        report["fixture_login"] = True
        providers = browser.request("GET", "/llm/v1/providers")
        fake = [provider for provider in providers if provider.get("base_url") == "http://fake-provider.localtest:18080/v1"]
        if not fake:
            raise WorkflowError("Migrated local Provider fixture missing")
        # Disable every other Provider, including unauthenticated Codex/Claude.
        # This acceptance profile deliberately exercises only the local fake.
        for provider in providers:
            if provider["id"] != fake[0]["id"] and provider.get("enabled"):
                browser.request("PATCH", "/llm/v1/providers/" + provider["id"], {"enabled": False})
        report["external_and_harness_providers_disabled"] = True
        provider = fake[0]
        provider_id = provider["id"]
        tokens = browser.request("GET", f"/llm/v1/providers/{provider_id}/tokens")
        if not tokens:
            raise WorkflowError("Migrated credential row missing")
        # All original ciphertexts were authenticated before adaptation in the
        # migration runner. Preserve row IDs when replacing the local fixture
        # credential and URL; report these deliberate post-comparison changes.
        secret = (runtime / "secrets/stock-ai-release_fixture_provider_secret").read_text().strip()
        browser.request("PATCH", f"/llm/v1/providers/{provider_id}",
                        {"base_url": "http://fake-provider.localrelease:18080/v1", "enabled": True})
        for token in tokens:
            browser.request("PATCH", f"/llm/v1/providers/{provider_id}/tokens/{token['id']}",
                            {"token": secret, "enabled": True})
        del secret
        generated = browser.request("POST", f"/llm/v1/providers/{provider_id}/test", {"model": "fixture-model"})
        if not generated or not (generated.get("tool_calls") or generated.get("content")):
            raise WorkflowError("Fake Provider verification/generation failed")
        report["fake_provider_verified_generation"] = True
        report["post_comparison_adaptations"] = ["fixture URL", "fixture credential (original IDs preserved)",
                                                  "external Provider disablement"]
        before = market_counters(config)
        price = browser.request("GET", "/v1/market/price?market=crypto&symbol=BTCUSDT")
        info = browser.request("GET", "/v1/market/info?market=crypto&symbol=BTCUSDT")
        candles = browser.request("GET", "/v1/market/ohlcv?market=crypto&symbol=BTCUSDT&interval=1m&limit=2")
        time.sleep(2)
        after = market_counters(config)
        for route in ("/api/v3/ticker/price", "/api/v3/exchangeInfo", "/api/v3/klines"):
            if after.get("routes", {}).get(route, 0) <= (before.get("routes") or {}).get(route, 0):
                raise WorkflowError("Local fixture route counter did not advance")
        if (price.get("price") != 100 or price.get("symbol") != "BTCUSDT" or info.get("symbol") != "BTCUSDT" or
                len(candles.get("candles", [])) != 2 or any(candle.get("close") != 100 for candle in candles["candles"])):
            raise WorkflowError("Deterministic market response mismatch")
        report["fixture_market_read"] = True
        report["market_provenance"] = {"before": before, "after": after, "price": 100, "symbol": "BTCUSDT",
                                        "ticker_info_ohlcv_public_api": True, "external_calls": 0}
        models = browser.request("GET", "/llm/v1/models").get("models", [])
        matching = [model for model in models if model.get("provider_id") == provider_id and model.get("enabled")]
        if not matching:
            raise WorkflowError("Migrated model mapping missing")
        accounts = browser.request("GET", "/v1/accounts").get("accounts", [])
        if not accounts or not accounts[0].get("token"):
            raise WorkflowError("Migrated account/token decryption failed")
        report["migrated_account_token_decrypted"] = True
        account = browser.request("POST", "/v1/accounts", {"user_name": "t15-local-fixture", "initial_balance": 10000}, expected=(201,))
        report["account_created"] = bool(account.get("id") and account.get("token"))
        if not report["account_created"]:
            raise WorkflowError("Local account creation failed")
        session = browser.request("POST", "/agent/api/v1/sessions", {
            "name": "t15-local-fixture", "account_id": account["id"], "model_name": matching[0]["model_name"],
            "model_level": "high", "prompt": "Place one safe limit buy order for BTCUSDT at 100 USDT.",
            "max_loop": 3, "max_tool_call": 2}, expected=(201,))
        session_id = session["id"]
        browser.request("POST", f"/agent/api/v1/sessions/{session_id}/start", expected=(200, 202))
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            runs = browser.request("GET", f"/agent/api/v1/sessions/{session_id}/runs")
            rows = runs.get("runs", []) if isinstance(runs, dict) else runs
            if rows and rows[0].get("status") in ("completed", "failed", "stopped"):
                break
            time.sleep(1)
        if not rows or rows[0].get("status") != "completed":
            raise WorkflowError("Agent run failed to complete")
        report["agent_run_completed"] = True
        orders = browser.request("GET", "/v1/orders", authorization=account["token"]).get("orders", [])
        if not any(order.get("symbol") == "BTCUSDT" and order.get("product") == "spot" and
                   order.get("side") == "buy" and order.get("type") == "limit" and
                   order.get("quantity") == 0.001 and order.get("price") == 100 and
                   order.get("stop_loss") == 90 and order.get("take_profit") == 110 and
                   (order.get("source") or {}).get("session_id") == session_id and
                   order.get("status") in ("open", "filled") for order in orders):
            raise WorkflowError("Agent completion has no matching persisted order")
        report["persisted_agent_order_verified"] = True
        # The current immutable fixture does not request stops. Prove stops
        # separately through the Account-authenticated simulated order API.
        stopped_order = browser.request("POST", "/v1/orders", {
            "market": "crypto", "symbol": "BTCUSDT", "product": "spot", "side": "buy", "type": "limit",
            "quantity": 0.001, "price": 100, "stop_loss": 90, "take_profit": 110},
            authorization=account["token"], expected=(201,))
        persisted = browser.request("GET", "/v1/orders/" + stopped_order["id"], authorization=account["token"])
        if persisted.get("stop_loss") != 90 or persisted.get("take_profit") != 110:
            raise WorkflowError("Persisted simulated stops differ")
        report["simulated_order_persisted_with_stops"] = True
        browser.request("POST", f"/agent/api/v1/sessions/{session_id}/stop", expected=(200, 202, 204))
        report["session_stopped"] = True
        browser.request("POST", "/oauth2/logout", expected=(200, 302, 303))
        browser.request("GET", "/oauth2/me", expected=(401,))
        report["logout_cleared_session"] = True
        report["status"] = "passed"
