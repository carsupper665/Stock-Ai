#!/usr/bin/env python3
"""Repeatable WSL/Linux sign-off. All destructive probes are scoped to Release.

Browser credentials and restored plaintext exist only in a private temporary
directory. Public reports contain statuses, hashes and counts, never tokens.
"""
import argparse
import copy
import hashlib
import json
import shutil
import socket
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.parse import urljoin

from accept import sync_bundle
from business import market_boundary, fixture_observation
from deploy import preflight, project_containers, snapshot, rollback_metadata, utc, persistent
from migrate import copy_volumes, store_facts, verified_archive, decrypt_credentials
from prepare import DEFAULT_MANIFEST, approved_images
from scripts import linux_recovery as recovery
from scripts.local_operations import Operator, WorkflowError, run
from signoff_http import Browser, ProbeError, fixture_control


def require(condition, message):
    if not condition:
        raise WorkflowError(message)


def login(browser):
    response, chain = browser.follow()
    require(response["status"] == 200 and any(row["path"] == "/oauth2/callback" for row in chain), "Login failed")
    require(browser.request("/v1/accounts")["status"] == 200, "Cookie login cannot access Backend")
    return chain


def denied(browser, headers=None):
    codes = {path: browser.request(path, headers=headers)["status"] for path in
             ("/v1/accounts", "/agent/api/v1/sessions", "/llm/v1/providers")}
    require(all(code in (401, 403) for code in codes.values()), "Private API authorization failed")
    return codes


def security(runtime, directory, config):
    results = {"anonymous": denied(Browser(runtime, directory / "anonymous"))}
    spoof = {"X-Auth-Request-Subject": "local-release-user", "X-Auth-Request-User": "local-release-user",
             "X-Auth-Request-Access-Token": "forged", "Forwarded": "proto=https;host=app.localrelease:8443",
             "X-Forwarded-Host": "app.localrelease:8443", "X-Original-URL": "/v1/accounts"}
    results["spoofed_headers"] = denied(Browser(runtime, directory / "spoof"), spoof)
    results["forged_bearer"] = denied(Browser(runtime, directory / "bearer"), {"Authorization": "Bearer forged"})
    for scenario in ("wrong-subject", "expired-token", "expired-access-token", "issuer-mismatch", "audience-mismatch"):
        fixture_control("next", {"scenario": scenario})
        actor = Browser(runtime, directory / scenario)
        response, chain = actor.follow()
        results[scenario] = {"redirect_chain": chain, "private_api_statuses": denied(actor)}
        print("Authorization rejection passed: " + scenario, flush=True)
    fixture_control("next", {"scenario": "short-token"})
    actor = Browser(runtime, directory / "short")
    response, chain = actor.follow()
    # The session itself must expire when the access token expires; no cached
    # cookie may authorize other services after its two-second lifetime.
    time.sleep(3)
    results["expired_session"] = denied(actor)
    first = Browser(runtime, directory / "replay-first")
    replay = Browser(runtime, directory / "replay-second")
    fixture_control("next", {"scenario": "success"})
    start = first.request("/oauth2/start")
    require(start["status"] == 302, "Replay setup start failed")
    authorization = first.request(start["headers"]["location"][0])
    require(authorization["status"] == 302, "Replay setup authorize failed")
    callback = authorization["headers"]["location"][0]
    shutil.copyfile(first.jar, replay.jar)
    response, chain = first.follow(callback)
    require(first.request("/v1/accounts")["status"] == 200, "First code redemption failed")
    response, rejected_chain = replay.follow(callback)
    results["replayed_callback"] = {"first_login_status": 200, "replay_redirect_chain": rejected_chain,
                                    "private_api_statuses": denied(replay), "independent_transaction_cookie_copy": True}
    results["backend_direct_rejections"] = fixture_observation(config, "backend-rejections")
    require(set(results["backend_direct_rejections"]["statuses"].values()) == {401}, "Direct Backend matrix failed")
    actor = Browser(runtime, directory / "positive")
    results["positive_redirect_chain"] = login(actor)
    response = actor.request("/")
    required = {"strict-transport-security", "content-security-policy", "x-content-type-options", "referrer-policy", "x-frame-options"}
    require(required <= set(response["headers"]), "Missing security header")
    require(set(response["headers"]["x-content-type-options"]) == {"nosniff"} and
            set(response["headers"]["x-frame-options"]) == {"DENY"}, "Security header value differs")
    require(response["headers"].get("server", [""])[0] in ("", "nginx"), "Version banner exposed")
    # Inspect the real session cookie attributes emitted by the gateway.
    cookie_lines = []
    for path in actor.directory.glob("headers-*"):
        cookie_lines += [line.partition(":")[2].strip() for line in path.read_text().splitlines() if line.lower().startswith("set-cookie:")]
    session = [line for line in cookie_lines if line.startswith("__Host-") and "Max-Age=-1" not in line]
    require(bool(session), "Host cookie missing")
    require(all("secure" in line.lower() and "httponly" in line.lower() and "samesite=" in line.lower()
                and "path=/" in line.lower() and "domain=" not in line.lower() for line in session), "Cookie attributes unsafe")
    oversized = actor.request("/v1/accounts", "POST", b"x" * (1024 * 1024 + 1))
    require(oversized["status"] == 413, "Ingress request body limit failed")
    results["ingress"] = {"verified_local_tls": True, "security_headers": sorted(required), "server_version_hidden": True,
                           "host_cookie_secure_httponly_samesite_path_no_domain": True, "oversized_body_status": 413}
    return results, actor


def surface(runtime, actor, config):
    entries = project_containers()
    published = []
    for entry in entries:
        service = entry["Config"]["Labels"]["com.docker.compose.service"]
        for target, bindings in entry["NetworkSettings"]["Ports"].items():
            for binding in bindings or []:
                published.append({"service": service, "target": target, **binding})
    require(published == [{"service": "nginx", "target": "8443/tcp", "HostIp": "127.0.0.1", "HostPort": "8443"}], "Unexpected Release port publication")
    closed = []
    for port in (4180, 7794, 8080, 8088, 8090, 18080, 18081):
        with socket.socket() as probe:
            probe.settimeout(1)
            require(probe.connect_ex(("127.0.0.1", port)) != 0, "Private service exposed on host")
        closed.append(port)
    listeners = run(["ss", "-H", "-ltn"]).stdout.splitlines()
    paths = ("/.env", "/.git/HEAD", "/backend.db", "/llm-provider.db", "/agent-server.db", "/logs/", "/data/",
             "/admin/", "/__fixture/next", "/__fixture/outage", "/assets/index.js.map", "/_oauth2_auth", "/oauth2/auth")
    homepage_bytes = actor.request("/")["body"]
    homepage = homepage_bytes.decode(errors="replace")
    import re
    actual_scripts = re.findall(r'<script[^>]+src="([^"]+\.js)"', homepage)
    require(actual_scripts, "Frontend JavaScript asset list missing")
    paths += tuple(path + ".map" for path in actual_scripts)
    known_secrets = [path.read_bytes().strip() for path in (runtime / "secrets").iterdir()]
    probes = []
    for path in paths:
        response = actor.request(path)
        body = response["body"]
        generic_html = (response["status"] == 200 and body == homepage_bytes and
                        b"<!doctype html" in body.lower() and
                        response["headers"].get("content-type", [""])[0].startswith("text/html"))
        require(response["status"] in (401, 403, 404) or generic_html, "Sensitive surface unexpectedly accessible: " + path)
        require(not any(marker in body for marker in (b"SQLite format 3", b"BEGIN PRIVATE KEY", b"Index of /", b'"sourcesContent"')), "Sensitive artifact leaked")
        require(not any(secret and secret in body for secret in known_secrets), "Private artifact leaked a runtime secret")
        probes.append({"path": path, "status": response["status"], "generic_spa_only": generic_html,
                       "body_sha256": hashlib.sha256(body).hexdigest()})
    bindings = [mount["Source"] for entry in entries for mount in entry["Mounts"] if mount["Type"] == "bind"]
    require(all("worktrees" not in Path(path).parts for path in bindings), "Disposable bind source remains")
    return {"status": "passed", "published_ports": published, "private_loopback_ports_closed": closed,
            "host_tcp_listeners": listeners, "probes": probes, "persistent_bind_sources": True,
            "market_boundary": market_boundary(config),
            "other_projects": "Existing Foreman/registry/Test listeners are recorded, outside this Release certification"}


def crash_backend(operator, config):
    before = operator.container("backend")
    require(before["Config"]["Labels"].get("com.docker.compose.project") == "stock-ai-local-release" and
            before["State"]["Running"] and before["State"]["Pid"] > 1, "Unexpected crash target")
    # Docker kill marks a container manually stopped and suppresses its restart
    # policy. Deliver the signal to the inspected process from the engine's PID
    # namespace instead, so this is an application crash. The immutable helper
    # has no network, mounts or capabilities except KILL, and one fixed target.
    run(["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "KILL",
         "--security-opt", "no-new-privileges:true", "--pid", "host", "--user", "0:0", "--entrypoint", "/bin/kill",
         config["services"]["nginx"]["image"], "-KILL", str(before["State"]["Pid"])])
    operator.wait_healthy(config)
    after = operator.container("backend")
    require(before["Id"] == after["Id"] and before["State"]["StartedAt"] != after["State"]["StartedAt"], "Crash restart not observed")
    require(after["RestartCount"] > before["RestartCount"], "Automatic crash restart not observed")
    return {"status": "passed", "same_container": True, "automatic_restart_count_advanced": True,
            "method": "kernel SIGKILL of inspected Release Backend process from immutable networkless helper",
            "manual_compose_start_needed": False}


def incidents(operator, config, runtime, actor, directory):
    crash = crash_backend(operator, config)
    require(actor.request("/v1/accounts")["status"] == 200, "Backend API did not recover")
    fixture_control("outage", {"enabled": True})
    try:
        outage = Browser(runtime, directory / "idp-down")
        response, chain = outage.follow()
        require(response["status"] >= 400, "Outage login unexpectedly succeeded")
        statuses = denied(outage)
    finally:
        fixture_control("outage", {"enabled": False})
    recovered = Browser(runtime, directory / "idp-recovered")
    login(recovered)
    providers = recovered.json("/llm/v1/providers")
    fake = [row for row in providers if row.get("base_url") == "http://fake-provider.localrelease:18080/v1" and row.get("enabled")]
    require(len(fake) == 1, "Enabled local provider missing")
    endpoint = "/llm/v1/providers/" + fake[0]["id"] + "/test"
    before_generation = recovered.json(endpoint, "POST", {"model": "fixture-model"})
    require(before_generation.get("content") or before_generation.get("tool_calls"), "Provider baseline failed")
    fixture = operator.container("idp")["Id"]
    run(["docker", "pause", fixture])
    try:
        try:
            response = recovered.request(endpoint, "POST", {"model": "fixture-model"}, retry=False)
            down_status = response["status"]
            require(down_status >= 400, "Provider outage unexpectedly succeeded")
        except ProbeError:
            down_status = "bounded-client-timeout"
    finally:
        run(["docker", "unpause", fixture])
    operator.wait_healthy(config)
    generated = recovered.json(endpoint, "POST", {"model": "fixture-model"})
    require(generated.get("content") or generated.get("tool_calls"), "Provider did not recover")
    return {"status": "passed", "backend_crash": crash, "backend_kill_restart_same_container": True, "backend_api_recovered": True,
            "identity_outage": {"fresh_login_status": chain[-1]["status"], "private_api_statuses": statuses, "login_recovered": True},
            "provider_outage": {"status": down_status, "generation_recovered": True,
                "method": "pause/unpause shared fixture; collocated IdP, market and adapter also temporarily unavailable"}}, recovered


def restore(operator, config, runtime, directory, backup):
    base = directory / "restore"
    base.mkdir(mode=0o700)
    stage = verified_archive(Path(backup["backup_file"]), runtime / "recovery.key", base, "stock-ai.complete-release-backup.v1")
    baseline = store_facts(stage)
    (stage / "keys").mkdir(mode=0o700)
    shutil.copyfile(stage / "secrets/stock-ai-release_llm_master_key", stage / "keys/llm_master_key")
    credentials = decrypt_credentials(stage)
    for path in (stage / "secrets").iterdir():
        path.chmod(0o440)
    (stage / "certs").chmod(0o750)
    for path in (stage / "certs").iterdir():
        path.chmod(0o440)
    project = "stock-ai-t16-restore-" + utc().strftime("%Y%m%d%H%M%S")
    clone = copy.deepcopy(config)
    clone["name"] = project
    for name, definition in clone["networks"].items():
        definition["name"] = project + "_" + name
    for name, definition in clone["volumes"].items():
        definition["name"] = project + "_" + name
    for definition in clone["secrets"].values():
        definition["file"] = str(stage / "secrets" / Path(definition["file"]).name)
    for service in clone["services"].values():
        for mount in service.get("volumes", []):
            if mount["type"] == "bind" and Path(mount["source"]).is_relative_to(runtime / "certs"):
                mount["source"] = str(stage / "certs" / Path(mount["source"]).relative_to(runtime / "certs"))
    clone["services"]["nginx"]["ports"][0]["published"] = "9444"
    compose = base / "compose.json"
    compose.write_text(json.dumps(clone))
    compose.chmod(0o600)
    check = Operator(operator.env_file, [compose], base / "evidence", 180)
    volumes = [row["name"] for row in clone["volumes"].values()]
    try:
        for name in volumes:
            run(["docker", "volume", "create", "--label", "stock-ai.disposable=t16-restore", name])
        helper = config["services"]["nginx"]["image"]
        for service, volume in recovery.VOLUMES.items():
            archive = base / (service + ".tar.gz")
            recovery.build_archive(stage / service, archive)
            command = ["docker", "run", "--rm", "-i", "--network", "none", "--read-only", "--cap-drop", "ALL",
                       "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER",
                       "--security-opt", "no-new-privileges:true", "--user", "0:0", "--mount",
                       f"type=volume,src={clone['volumes'][volume]['name']},dst=/data", "--entrypoint", "/bin/sh", helper,
                       "-ec", "tar -xzf - -C /data --no-same-owner; chown -R 65532:1000 /data; find /data -type d -exec chmod 700 {} +; find /data -type f -exec chmod 600 {} +"]
            with archive.open("rb") as source:
                response = subprocess.run(command, stdin=source, capture_output=True, timeout=120)
            require(response.returncode == 0, "Disposable volume restore failed")
        comparison = base / "comparison"
        comparison.mkdir(mode=0o700)
        copy_volumes(clone, helper, comparison)
        require(store_facts(comparison) == baseline, "Restored rows/schema/IDs/history differ before startup")
        check.compose_run("up", "-d", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "180", timeout=240)
        check.wait_healthy(clone)
        browser = Browser(runtime, base / "browser", port=9444)
        chain = login(browser)
        accounts = browser.json("/v1/accounts").get("accounts", [])
        require(accounts and accounts[0].get("token"), "Restored account encryption key mismatch")
        providers = browser.json("/llm/v1/providers")
        fake = [row for row in providers if row.get("base_url") == "http://fake-provider.localrelease:18080/v1" and row.get("enabled")]
        require(len(fake) == 1, "Restored provider missing")
        generated = browser.json("/llm/v1/providers/" + fake[0]["id"] + "/test", "POST", {"model": "fixture-model"})
        require(generated.get("content") or generated.get("tool_calls"), "Restored provider encryption key mismatch")
        result = {"status": "passed", "backup_sha256": backup["sha256"], "authenticated_inventory": True,
                  "all_rows_schema_ids_history_match_before_writers_start": True, "baseline": baseline,
                  "provider_ciphertexts_authenticated": credentials, "account_token_decryption": True,
                  "provider_generation": True, "seven_restored_services_healthy": True, "redirect_chain": chain,
                  "live_release_volumes_modified": False, "restore_project": project}
    finally:
        check.compose_run("down", "--timeout", "15", check=False, timeout=120)
        for name in volumes:
            run(["docker", "volume", "rm", name], check=False)
        require(not run(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + project]).stdout.strip(), "Restore containers remain")
        require(not run(["docker", "network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project]).stdout.strip(), "Restore networks remain")
        require(all(run(["docker", "volume", "inspect", name], check=False).returncode != 0 for name in volumes), "Restore volumes remain")
    result["disposable_containers_networks_volumes_removed"] = True
    return result


def soak(operator, config, runtime, browser, seconds):
    start = utc()
    deadline = time.monotonic() + seconds
    observations = []
    while True:
        entries = project_containers()
        healthy = all(entry["State"]["Status"] == "running" and entry["State"].get("Health", {}).get("Status") == "healthy" for entry in entries)
        require(len(entries) == 7 and healthy, "Persistent health failure during soak")
        price = browser.json("/v1/market/price?market=crypto&symbol=BTCUSDT")
        require(price.get("price") == 100, "Market read failed during soak")
        observations.append({"elapsed_seconds": round((utc() - start).total_seconds(), 2), "healthy_services": 7, "price": 100})
        if time.monotonic() >= deadline:
            break
        time.sleep(min(10, max(0, deadline - time.monotonic())))
    secrets = [path.read_bytes().strip() for path in (runtime / "secrets").iterdir()]
    log_sizes = {}
    for entry in project_containers():
        service = entry["Config"]["Labels"]["com.docker.compose.service"]
        response = subprocess.run(["docker", "logs", "--since", start.isoformat(), entry["Id"]], capture_output=True, timeout=30)
        require(response.returncode == 0, "Container log inspection failed")
        content = response.stdout + response.stderr
        require(not any(secret and secret in content for secret in secrets), "Runtime secret found in logs")
        log_sizes[service] = len(content)
        require(len(content) <= 2 * 1024 * 1024, "Uncontrolled container log growth")
    return {"status": "passed", "requested_seconds": seconds, "observations": observations,
            "container_log_bytes": log_sizes, "known_runtime_secrets_absent_from_logs": True,
            "per_container_log_growth_limit_bytes": 2 * 1024 * 1024, "external_market_calls": 0}


def execute(args):
    runtime = persistent(args.runtime)
    bundle = runtime / "deployment"
    sync_bundle(bundle)
    files = [bundle / "deploy/compose" / name for name in ("compose.yaml", "compose.local-release.yaml")]
    operator = Operator(runtime / "compose.env", files, args.output, 180)
    report = {"schema": "stock-ai.local-release-signoff.v1", "status": "running", "started_at_utc": utc().isoformat()}
    def record(stage, value):
        operator.write_evidence(stage + ".json", value)
        report[stage] = {"status": "passed", "report": stage + ".json"}
        operator.write_evidence("summary.json", report)
        print(stage + " passed", flush=True)
    preflight_report, config, _ = preflight(operator, DEFAULT_MANIFEST)
    record("preflight", preflight_report)
    with tempfile.TemporaryDirectory(prefix="t16-private-", dir=runtime) as temporary:
        directory = Path(temporary)
        directory.chmod(0o700)
        security_report, actor = security(runtime, directory / "http", config)
        record("security", security_report)
        record("surface", surface(runtime, actor, config))
        incident_report, actor = incidents(operator, config, runtime, actor, directory)
        record("incidents", incident_report)
        backup = snapshot(operator, config, project_containers(), runtime / "backups", runtime / "recovery.key")
        record("backup", backup)
        record("restore", restore(operator, config, runtime, directory, backup))
        record("soak", soak(operator, config, runtime, actor, args.soak_seconds))
        # A final consistent backup checks all live SQLite stores after soak.
        final = snapshot(operator, config, project_containers(), runtime / "backups", runtime / "recovery.key")
        record("accepted-backup", final)
        for database in final["databases"].values():
            require(database["integrity_check"] == "ok", "Post-soak database integrity failed")
        scan = json.loads((args.output / "scans/summary.json").read_text())
        images, manifest_hash = approved_images(DEFAULT_MANIFEST)
        require(scan["status"] == "passed" and {name: row["image"] for name, row in scan["images"].items()} == images, "Accepted scan differs from current images")
        for row in scan["images"].values():
            require(hashlib.sha256((args.output / "scans" / row["report"]).read_bytes()).hexdigest() == row["sha256"], "Scan artifact hash differs")
        record("rollback", rollback_metadata(operator, config, DEFAULT_MANIFEST))
        rate = Browser(runtime, directory / "rate")
        codes = [rate.request("/oauth2/start", retry=False)["status"] for _ in range(20)]
        require(429 in codes, "Login rate limiter failed")
        record("rate-limit", {"status": "passed", "requests": len(codes), "statuses": codes, "rate_limited": True})
        operator.wait_healthy(config)
        engine = json.loads(run(["docker", "info", "--format", "{{json .}}"] ).stdout)
        nginx_version = run(["docker", "exec", operator.container("nginx")["Id"], "nginx", "-v"])
        report.update({"status": "passed", "completed_at_utc": utc().isoformat(), "accepted_images": images,
                       "promotion_manifest_sha256": manifest_hash, "accepted_backup_sha256": final["sha256"],
                       "versions": {"docker_engine": engine["ServerVersion"], "nginx": (nginx_version.stdout + nginx_version.stderr).strip()},
                       "high_critical_count": 0, "all_seven_services_healthy": True,
                       "scope": "WSL/Linux loopback; local identity, provider, Binance and scoped adapter fixtures",
                       "non_gating": ["external IdP", "public DNS/TLS renewal", "Windows/reboot", "long-duration production soak"]})
        operator.write_evidence("summary.json", report)
        print("Local Release sign-off passed", flush=True)
    for path in args.output.rglob("*"):
        if path.is_file() and path.name != "SHA256SUMS":
            path.chmod(0o644)
    lines = [hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.relative_to(args.output).as_posix()
             for path in sorted(args.output.rglob("*")) if path.is_file() and path.name != "SHA256SUMS"]
    (args.output / "SHA256SUMS").write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--soak-seconds", type=int, default=120)
    args = parser.parse_args()
    require(args.soak_seconds >= 120, "Soak must run at least 120 seconds")
    try:
        execute(args)
    except Exception as error:
        args.output.mkdir(parents=True, exist_ok=True)
        (args.output / "failure.json").write_text(json.dumps({"status": "failed", "error_type": type(error).__name__,
            "reason": str(error) if isinstance(error, WorkflowError) else "Private diagnostics suppressed",
            "at_utc": utc().isoformat()}) + "\n")
        raise SystemExit("Local sign-off failed closed; inspect sanitized failure.json")
