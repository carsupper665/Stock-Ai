#!/usr/bin/env python3
"""Execute the manual's curl and Python examples through the real middleware."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.parse import parse_qs, urlsplit
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[2]
MANUAL = ROOT / "docs/api/account-api.md"


class MarketFixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        url = urlsplit(self.path)
        query = parse_qs(url.query)
        symbol = query.get("symbol", ["BTCUSDT"])[0]
        if url.path.endswith("ticker/price"):
            body = {"symbol": symbol, "price": "60000"}
        elif url.path.endswith("klines"):
            start = int(query["startTime"][0])
            body = [[start, "60000", "60100", "59900", "60000", "10", start + 59999, "0", 1, "0", "0", "0"]]
        elif url.path.endswith("exchangeInfo"):
            body = {"symbols": [{"symbol": symbol, "status": "TRADING", "baseAsset": "BTC", "quoteAsset": "USDT",
                                  "orderTypes": ["MARKET", "LIMIT"], "isSpotTradingAllowed": True,
                                  "isMarginTradingAllowed": True, "filters": []}]}
        else:
            self.send_error(404)
            return
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def request(base, path, token=None, method="GET", body=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    data = None if body is None else json.dumps(body).encode()
    req = Request(base + path, data=data, headers=headers, method=method)
    try:
        response = urlopen(req, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        raw = response.read()
        return response.status, json.loads(raw) if raw else None


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_ready(process, base, path):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise AssertionError(f"service exited: {process.returncode}")
        try:
            if request(base, path)[0] == 200:
                return
        except OSError:
            pass
        time.sleep(0.05)
    raise AssertionError(f"service not ready: {base}{path}")


def assert_shape(example, actual, path="response"):
    if isinstance(example, dict):
        assert isinstance(actual, dict), path
        for key, value in example.items():
            assert key in actual, f"undocumented response field {path}.{key}"
            assert_shape(value, actual[key], path + "." + key)
    elif isinstance(example, list):
        assert isinstance(actual, list), path
        if example:
            assert actual, path
            assert_shape(example[0], actual[0], path + "[]")
    elif example is None:
        assert actual is None, path
    else:
        assert type(example) is type(actual), path


def check_routes(manual):
    router = (ROOT / "backend/internal/api/router.go").read_text()
    account_routes = set()
    user_routes = set()
    for receiver, method, path, rest in re.findall(r'(v1|trade|admin)\.(GET|POST|PATCH|DELETE)\("([^"]*)",([^\n]+)', router):
        full_path = "/v1" + ("/accounts" if receiver == "admin" else "") + path
        route = (method, full_path)
        if receiver == "trade" or "requireAny" in rest or "optionalAuth" in rest:
            account_routes.add(route)
        elif receiver == "admin" or "requireUser" in rest:
            user_routes.add(route)
    documented = set(re.findall(r'### `(GET|POST|PATCH|DELETE) ([^`]+)`', manual))
    assert documented == account_routes, (documented ^ account_routes)
    assert len(account_routes) == 17
    table = manual.split("## 只給 USER 的端點")[1].split("## 完整 Python 範例")[0]
    documented_user = set()
    for methods, path in re.findall(r'\| ([A-Z /]+) \| `([^`]+)` \|', table):
        documented_user.update((method.strip(), path) for method in methods.split("/"))
    assert documented_user == user_routes, documented_user ^ user_routes
    assert len(user_routes) == 16
    return account_routes


def check_release(baseline, package, output):
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=ROOT, text=True)

    def views(source):
        return {name: set(re.findall(r'json:"([^",]+)', body))
                for name, body in re.findall(r'type (\w+View) struct \{(.*?)\n\}', source, re.S)}

    for path in ("backend/internal/api/account.go", "backend/internal/api/views.go", "backend/internal/api/message.go"):
        previous = views(git("show", baseline + ":" + path))
        current = views((ROOT / path).read_text())
        for name, fields in previous.items():
            assert fields <= current[name], (path, name, fields - current[name])
    output.append("PASS TestPublicApiFieldsRemainAdditive")
    for module in ("backend", "agent_server", "llm_provider_server", "middleware"):
        assert not git("diff", baseline + "...HEAD", "--", module + "/go.mod", module + "/go.sum")
    assert not git("diff", baseline + "...HEAD", "--", "frontend/package.json", "frontend/package-lock.json")
    # 群聊推播：middleware 只多一個測試，nginx 多一條 SSE 路由
    deployment_changes = git("diff", "--name-only", baseline + "...HEAD", "--", "middleware", "deploy", "docker").splitlines()
    assert set(deployment_changes) <= {
        "middleware/VERSION", "middleware/internal/gateway/proxy_test.go",
        "deploy/nginx/release-edge.conf.template", "deploy/nginx/test-edge.conf.template"}
    output.append("PASS TestChangeScope: no new dependencies; middleware code unchanged; deployment only adds the chat stream route")
    if package is None:
        return

    def hashes(folder):
        return {str(path.relative_to(folder)): hashlib.sha256(path.read_bytes()).hexdigest()
                for path in folder.rglob("*") if path.is_file()}

    assert hashes(ROOT / "frontend/dist") == hashes(package / "web"), "Windows frontend differs from the build"
    executables = {}
    for name in ("backend.exe", "agent-server.exe", "llm-provider-server.exe", "middleware.exe"):
        data = (package / name).read_bytes()
        assert data[:2] == b"MZ" and len(data) > 1_000_000, name
        executables[name] = hashlib.sha256(data).hexdigest()
    report = {"windows_package": str(package), "versions": {name: (ROOT / name / "VERSION").read_text().splitlines()[0]
              for name in ("backend", "agent_server", "frontend")}, "executables": executables,
              "web_files_equal": True, "web_file_count": len(hashes(package / "web")), "public_api_fields_additive": True,
              "no_dependency_changes": True, "middleware_deployment_changes": deployment_changes}
    (ROOT / "docs/api/windows-test-package.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    output.append("PASS TestWindowsPackageMatchesBuild: all web hashes match; four valid Windows executables")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--record", action="store_true", help="refresh the manual's response examples")
    parser.add_argument("--output", type=Path, default=ROOT / "docs/api/account-api-smoke.txt")
    parser.add_argument("--baseline", help="commit used for the release compatibility audit")
    parser.add_argument("--windows-package", type=Path, help="also compare a rebuilt Windows test package")
    args = parser.parse_args()
    manual = MANUAL.read_text()
    check_routes(manual)
    output = ["PASS TestAccountApiManualRoutes: 17 account routes, 16 USER routes"]
    if args.windows_package:
        assert args.baseline, "--windows-package requires --baseline"
    if args.baseline:
        check_release(args.baseline, args.windows_package, output)
    processes = []
    ports = []
    with tempfile.TemporaryDirectory(prefix="account-api-") as folder:
        work = Path(folder)
        source = ThreadingHTTPServer(("127.0.0.1", 0), MarketFixture)
        ports.append(source.server_port)
        thread = threading.Thread(target=source.serve_forever, daemon=True)
        thread.start()
        try:
            for module, target, package, flags in [
                ("backend", "backend", "./test/market_cross_service_host", []),
                ("middleware", "middleware", "./cmd/middleware", ["-tags", "wintest"]),
            ]:
                subprocess.run(["go", "build", *flags, "-o", str(work / target), package], cwd=ROOT / module, check=True)
            env = os.environ.copy()
            # Every credential is generated for this run, never read from the user's installation.
            for name in ("USER_TOKEN", "AGENT_SERVICE_TOKEN", "ACCOUNT_TOKEN_KEY", "BACKEND_USER_TOKEN", "AGENT_SERVER_ADMIN_TOKEN", "LLM_SERVER_ADMIN_TOKEN", "LLM_SERVER_RUNTIME_TOKEN"):
                env.pop(name, None)
                env.pop(name + "_FILE", None)
            for logical, names in [
                ("backend_middleware_token", ["USER_TOKEN", "BACKEND_USER_TOKEN"]),
                ("backend_user_token", ["AGENT_SERVICE_TOKEN"]),
                ("backend_account_token_key", ["ACCOUNT_TOKEN_KEY"]),
                ("agent_admin_token", ["AGENT_SERVER_ADMIN_TOKEN"]),
                ("llm_admin_token", ["LLM_SERVER_ADMIN_TOKEN"]),
                ("llm_runtime_token", ["LLM_SERVER_RUNTIME_TOKEN"]),
            ]:
                path = work / ("stock-ai-test_" + logical)
                path.write_text(base64.b64encode(secrets.token_bytes(32)).decode() if logical.endswith("_key") else secrets.token_urlsafe(48))
                path.chmod(0o600)
                for name in names:
                    env[name + "_FILE"] = str(path)
            env.update(STOCK_AI_ENVIRONMENT="Test", DB_DRIVER="sqlite", DB_DSN=str(work / "backend.db"),
                       LOG_DIR=str(work / "logs"), USER_NAME="USER", WEB_DIR=str(ROOT / "frontend/dist"),
                       MARKET_CROSS_SERVICE_SOURCE_URL=f"http://127.0.0.1:{source.server_port}")
            with (work / "backend.log").open("w+") as backend_log, (work / "middleware.log").open("w+") as middleware_log:
                backend = subprocess.Popen([str(work / "backend")], cwd=work, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=backend_log, text=True)
                processes.append(backend)
                # The host publishes its ephemeral port only after migration and service construction.
                line = backend.stdout.readline().strip()
                assert line.startswith("READY http://"), (line, (work / "backend.log").read_text())
                backend_base = line.removeprefix("READY ")
                ports.append(urlsplit(backend_base).port)
                middleware_port = unused_port()
                ports.append(middleware_port)
                base = f"http://127.0.0.1:{middleware_port}"
                env.update(BACKEND_URL=backend_base, AGENT_SERVER_URL=backend_base, LLM_SERVER_URL=backend_base, MIDDLEWARE_ADDR=f"127.0.0.1:{middleware_port}")
                middleware = subprocess.Popen([str(work / "middleware")], cwd=work, env=env, stdout=middleware_log, stderr=middleware_log)
                processes.append(middleware)
                wait_ready(middleware, base, "/healthz")
                status, account = request(base, "/v1/accounts", method="POST", body={"user_name": "Manual Agent", "initial_balance": 10000})
                assert status == 201, account
                token = account["token"]
                sample_env = env | {"BASE": base, "TOKEN": token}
                blocks = re.findall(r'```bash\n(curl [\s\S]*?)\n```', manual)
                assert len(blocks) == 19, len(blocks)
                responses = {}
                for index, command in enumerate(blocks, 1):
                    completed = subprocess.run(["bash", "-c", command + " -w '\\n%{http_code}'"], cwd=work, env=sample_env, capture_output=True, text=True, timeout=20)
                    assert completed.returncode == 0, (command, completed.stderr, completed.stdout)
                    raw, status_text = completed.stdout.rsplit("\n", 1)
                    status = int(status_text)
                    assert status in (200, 201, 204), (command, completed.stdout)
                    body = json.loads(raw) if raw else None
                    output.extend([f"curl {index}: {command}", f"HTTP {status}", json.dumps(body, ensure_ascii=False) if body is not None else "<empty>"])
                    method = re.search(r'-X (\w+)', command)
                    method = method[1] if method else ("POST" if " -d " in command else "GET")
                    path = re.search(r'\$BASE([^"?]+)', command)[1]
                    path = re.sub(r'\$(ORDER_ID|CANCEL_ID|POSITION_ID|MESSAGE_ID)', ':id', path)
                    if body is not None:
                        responses.setdefault((method, path), body)
                    if method == "POST" and path == "/v1/orders":
                        if body["type"] == "market":
                            sample_env["ORDER_ID"] = body["id"]
                            sample_env["POSITION_ID"] = body["position_id"]
                        else:
                            assert body["status"] == "open", body
                            sample_env["CANCEL_ID"] = body["id"]
                    if method == "POST" and path == "/v1/messages":
                        sample_env["MESSAGE_ID"] = body["id"]
                pattern = r'(<!-- response: (\w+) ([^ ]+) -->\n```json\n)([\s\S]*?)(\n```)'
                def verify_or_record(match):
                    actual = responses[(match[2], match[3])]
                    if not args.record:
                        assert_shape(json.loads(match[4]), actual)
                    return match[1] + json.dumps(actual, ensure_ascii=False, indent=2) + match[5] if args.record else match[0]
                refreshed = re.sub(pattern, verify_or_record, manual)
                if args.record:
                    MANUAL.write_text(refreshed)
                # Real token path through middleware, including USER-only and private services.
                for method, path in (("GET", "/v1/accounts"), ("DELETE", "/v1/messages"), ("GET", "/v1/messages/stream"),
                                     ("GET", "/agent/api/v1/sessions"), ("GET", "/llm/v1/models")):
                    status, body = request(base, path, token, method)
                    assert status == 403 and body["error"] == "forbidden", (path, status, body)
                    output.append(f"PASS AccountTokenForbidden {method} {path}: HTTP {status} {json.dumps(body, ensure_ascii=False)}")
                code = re.search(r'```python\n([\s\S]*?)\n```', manual)[1]
                script = work / "account-example.py"
                script.write_text(code)
                completed = subprocess.run(["python3", str(script)], env=sample_env, capture_output=True, text=True, timeout=30)
                assert completed.returncode == 0, completed.stderr
                output.extend(["PASS TestAccountApiPythonFlow", completed.stdout.strip()])
                assert request(base, "/v1/positions", token)[1]["positions"] == []
                status, short = request(base, "/v1/orders", token, "POST", {
                    "symbol": "BTCUSDT", "product": "futures", "side": "sell", "quantity": 0.001,
                })
                assert status == 201 and short["position_id"], short
                orders_before = request(base, "/v1/orders", token)[1]
                completed = subprocess.run(["python3", str(script)], env=sample_env, capture_output=True, text=True, timeout=30)
                assert completed.returncode != 0 and "沒有 BTCUSDT futures 持倉" in completed.stderr
                assert request(base, "/v1/orders", token)[1] == orders_before, "example traded despite existing position"
                assert request(base, "/v1/positions/" + short["position_id"] + "/close", token, "POST", {})[0] == 200
                output.append("PASS TestAccountApiPythonRejectsExistingPosition: fails before trading")
                output.append("PASS TestAccountApiCurlExamples: 19/19")
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    if process.stdin:
                        process.stdin.close()
                    else:
                        process.terminate()
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait(timeout=5)
            source.shutdown()
            source.server_close()
            thread.join(timeout=5)
            for port in ports:
                with socket.socket() as sock:
                    assert sock.connect_ex(("127.0.0.1", port)) != 0, f"port still listening: {port}"
            output.append("PASS TestSmokeCleanup: all three ports released, all service processes stopped")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text("\n".join(output) + "\n")
    print("\n".join(line for line in output if line.startswith("PASS")))
    print(f"Evidence: {args.output}")


if __name__ == "__main__":
    main()
