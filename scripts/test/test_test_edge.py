import json
import re
import unittest
from pathlib import Path

import yaml


ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "deploy/compose/compose.test-edge.yaml"
NGINX_TEMPLATE = ROOT / "deploy/nginx/test-edge.conf.template"
NGINX_MAIN = ROOT / "deploy/nginx/test-nginx.conf"
GATEWAY_DOC = ROOT / "docs/deployment/test-edge-identity.md"
EXTERNAL_CHECK = ROOT / "scripts/check-test-edge.ps1"
DOCKERIGNORE = ROOT / ".dockerignore"
EVIDENCE = ROOT / "deploy/evidence/test-edge-identity.not-run.json"


class TestEdgePolicyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.compose = yaml.safe_load(COMPOSE.read_text(encoding="utf-8"))
        cls.services = cls.compose["services"]
        cls.nginx = NGINX_TEMPLATE.read_text(encoding="utf-8")

    def test_public_ports_belong_only_to_nginx(self):
        published = {
            name: service.get("ports", [])
            for name, service in self.services.items()
            if service.get("ports")
        }
        self.assertEqual(set(published), {"nginx"})
        self.assertEqual(len(published["nginx"]), 2)
        self.assertRegex(" ".join(published["nginx"]), r":80:8080")
        self.assertRegex(" ".join(published["nginx"]), r":443:8443")
        self.assertNotIn("ports", self.services["middleware"])

    def test_middleware_uses_isolated_test_client_and_exact_https_urls(self):
        env = self.services["middleware"]["environment"]
        self.assertEqual(env["STOCK_AI_ENVIRONMENT"], "Test")
        self.assertEqual(env["MIDDLEWARE_ADDR"], ":4180")
        self.assertEqual(env["OIDC_CLIENT_ID"], "stock-ai-test")
        self.assertEqual(env["OIDC_COOKIE_NAME"], "__Host-stock-ai-test-session")
        self.assertTrue(env["OIDC_ISSUER"].startswith("${OIDC_ISSUER"))
        self.assertTrue(env["OIDC_JWKS_URL"].startswith("${OIDC_JWKS_URL"))
        self.assertTrue(env["OIDC_REDIRECT_URI"].startswith("https://${TEST_APP_HOST"))
        self.assertNotIn("secrets", self.services["middleware"])
        self.assertNotIn("secrets", self.compose)
        self.assertEqual(
            set(self.services["nginx"]["depends_on"]),
            {"middleware"},
        )
        for dependency in self.services["nginx"]["depends_on"].values():
            self.assertEqual(dependency["condition"], "service_healthy")

    def test_forwarded_headers_are_replaced_and_only_middleware_authenticates(self):
        main = NGINX_MAIN.read_text(encoding="utf-8")
        for header in (
            "X-Real-IP $remote_addr",
            "X-Forwarded-For $remote_addr",
            "X-Forwarded-Proto https",
            "X-Forwarded-Host ${TEST_APP_HOST}",
            'Authorization ""',
            'X-Forwarded-Port ""',
            'X-Forwarded-Server ""',
            'X-Forwarded-Prefix ""',
            'Forwarded ""',
            'X-Original-URL ""',
            'X-Original-URI ""',
            "Cookie $http_cookie",
            "Authorization $http_authorization",
        ):
            self.assertIn(f"proxy_set_header {header};", self.nginx)
        self.assertNotIn("auth_request", self.nginx)
        self.assertNotIn("X-Auth-Request", self.nginx)
        self.assertNotIn("stock_ai_console", self.nginx)
        self.assertNotIn("stock_ai_gateway", self.nginx)
        self.assertNotIn("oauth2/auth", self.nginx)
        self.assertNotIn("$http_authorization != \"\"", self.nginx)
        self.assertNotIn("$console_authorization", self.nginx + main)
        self.assertEqual(
            re.findall(r"upstream (\w+) \{ server ([^;]+);", self.nginx),
            [("stock_ai_middleware", "middleware:4180")],
        )
        self.assertEqual(set(re.findall(r"proxy_pass (http://[\w]+)", self.nginx)), {"http://stock_ai_middleware"})
        self.assertGreaterEqual(self.nginx.count("proxy_pass_request_headers off;"), 5)
        self.assertIn('location = /healthz', self.nginx)
        self.assertIn('location ^~ /assets/', self.nginx)
        self.assertIn('location ~ ^/v1(?:/|$)', self.nginx)
        self.assertIn('location ^~ /agent/', self.nginx)
        self.assertIn('location ^~ /llm/', self.nginx)
        self.assertIn("root /usr/share/nginx/html;", self.nginx)
        self.assertIn("try_files $uri /index.html;", self.nginx)
        stream = self.nginx.split("location = /v1/messages/stream {", 1)[1].split("\n    }", 1)[0]
        self.assertIn("proxy_buffering off;", stream)
        self.assertIn("proxy_read_timeout 1h;", stream)
        self.assertIn("proxy_set_header Authorization $http_authorization;", stream)
        for route in ("location ~ ^/v1(?:/|$) {", "location = /v1/messages/stream {", "location ^~ /agent/ {", "location ^~ /llm/ {", "location ^~ /oauth2/ {"):
            block = self.nginx.split(route, 1)[1].split("\n    }", 1)[0]
            self.assertIn("proxy_set_header Cookie $http_cookie;", block, route)
            self.assertIn("proxy_pass http://stock_ai_middleware;", block, route)
        static_root = self.nginx.split("location / {", 1)[1].split("\n    }", 1)[0]
        self.assertNotIn("proxy_pass", static_root)

    def test_edge_limits_and_security_headers_are_in_configuration(self):
        config = self.nginx + NGINX_MAIN.read_text(encoding="utf-8")
        for policy in (
            "client_max_body_size 1m",
            "limit_req_zone $binary_remote_addr zone=per_ip",
            "limit_req_zone $binary_remote_addr zone=login_per_ip",
            "limit_conn_zone $binary_remote_addr zone=connections_per_ip",
            "limit_req_status 429",
            'add_header Strict-Transport-Security "max-age=31536000" always',
            "Content-Security-Policy",
            'add_header X-Content-Type-Options "nosniff" always',
            'add_header Referrer-Policy "same-origin" always',
            'add_header X-Frame-Options "DENY" always',
            "ssl_protocols TLSv1.2 TLSv1.3",
        ):
            self.assertIn(policy, config)
        self.assertGreaterEqual(self.nginx.count("limit_req zone="), 4)

    def test_nginx_access_log_never_formats_request_query(self):
        config = NGINX_MAIN.read_text(encoding="utf-8")
        match = re.search(r"log_format edge_sanitized '([^']+)';", config)
        self.assertIsNotNone(match)
        log_format = match.group(1)
        self.assertIn("$request_method", log_format)
        self.assertIn("$uri", log_format)
        self.assertIn("$status", log_format)
        self.assertTrue(log_format.endswith("$request_time"))
        variables = re.findall(r"\$[a-z0-9_]+", log_format)
        for query_bearing_variable in ("$request", "$request_uri", "$args", "$query_string", "$arg_code", "$arg_state"):
            self.assertNotIn(query_bearing_variable, variables)
        self.assertIn("access_log /dev/stdout edge_sanitized;", config)

        marker = "query-marker-must-not-be-logged"
        values = {
            "$remote_addr": "192.0.2.4",
            "$time_iso8601": "2026-10-13T12:00:00+00:00",
            "$request_method": "GET",
            "$uri": "/oauth2/callback",
            "$server_protocol": "HTTP/1.1",
            "$status": "302",
            "$body_bytes_sent": "0",
            "$request_time": "0.002",
            "$request_uri": f"/oauth2/callback?code={marker}&state={marker}",
            "$args": f"code={marker}&state={marker}",
            "$query_string": f"code={marker}&state={marker}",
            "$arg_code": marker,
            "$arg_state": marker,
        }
        rendered = re.sub(r"\$[a-z0-9_]+", lambda found: values.get(found.group(), ""), log_format)
        self.assertIn('"GET /oauth2/callback HTTP/1.1" 302', rendered)
        self.assertNotIn(marker, rendered)

    def test_middleware_preflight_has_negative_unit_cases(self):
        gateway_tests = ROOT / "middleware/internal/gateway"
        checks = (gateway_tests / "external_idp_test.go").read_text(encoding="utf-8")
        for case in (
            "func TestPreflightReportsExternalIdPMismatches",
            "func TestPreflightExplainsIncompleteCertificateChain",
            '"issuer mismatch"',
            '"token URL mismatch"',
            '"auth method missing"',
            '"redirect not registered"',
            '"token endpoint on another origin"',
            '"plain http issuer"',
        ):
            self.assertIn(case, checks)
        self.assertIn("func TestConfigRejectsLoopbackAndLocalhostAliasesInBothEnvironments",
                      (gateway_tests / "gateway_test.go").read_text(encoding="utf-8"))

    def test_live_probe_covers_matrix_route_classes_and_role_rejection(self):
        script = EXTERNAL_CHECK.read_text(encoding="utf-8")
        gateway_dockerfile = (ROOT / "docker/middleware.Dockerfile").read_text(encoding="utf-8")
        for check in (
            '"https://$AppHost/healthz"',
            '"https://$AppHost/assets/app.js"',
            "'/v1/health', '/agent/health', '/llm/healthz'",
            '"https://$AppHost/v1/account"',
            "application/json",
            "'/agent/api/v1/sessions', '/llm/v1/providers'",
            "spoofed identity and forwarding headers do not authenticate",
        ):
            self.assertIn(check, script)
        self.assertIn("middleware/internal/**/*_test.go", DOCKERIGNORE.read_text(encoding="utf-8"))
        self.assertIn("COPY middleware/ ./", gateway_dockerfile)
        self.assertNotIn("issuer-check", gateway_dockerfile)

    def test_http_redirect_and_auth_routes_preserve_browser_contract(self):
        self.assertIn("return 301 https://${TEST_APP_HOST}$request_uri;", self.nginx)
        self.assertEqual(self.nginx.count("if ($host != ${TEST_APP_HOST}) { return 421; }"), 2)
        self.assertNotIn("proxy_set_header Host $host;", self.nginx)
        self.assertNotIn("proxy_set_header X-Forwarded-Host $host;", self.nginx)
        self.assertIn("location = /oauth2/callback", self.nginx)
        self.assertIn("location = /oauth2/start", self.nginx)
        self.assertIn("location ^~ /oauth2/", self.nginx)
        self.assertIn("server_name ${TEST_APP_HOST};", self.nginx)

    def test_operator_instructions_separate_static_review_from_live_acceptance(self):
        doc = GATEWAY_DOC.read_text(encoding="utf-8").lower()
        for required in (
            "docker compose",
            "stock-ai-test",
            "stock-ai-test_oidc_client_secret",
            "stock-ai-test_oidc_cookie_key",
            "middleware preflight",
            "evidence",
            "not run",
        ):
            self.assertIn(required, doc)
        self.assertRegex(doc, re.compile(r"https://[^\s]+/realms/stock-ai-test"))

    def test_evidence_distinguishes_local_checks_from_unavailable_test_host(self):
        evidence = json.loads(EVIDENCE.read_text(encoding="utf-8"))
        self.assertEqual(evidence["status"], "not_run")
        self.assertEqual(
            evidence["local_checks"]["policy_unittests"],
            "passed: scripts.test.test_test_edge (13 tests)",
        )
        self.assertIn("not installed", evidence["local_checks"]["external_probe_powershell_parse"])
        self.assertEqual(evidence["test_host_checks"]["nginx_and_application_startup"], "not_run")
        self.assertFalse(evidence["secret_or_personal_data_retained"])

    def test_external_probe_records_sanitized_full_port_and_oidc_checks(self):
        script = EXTERNAL_CHECK.read_text(encoding="utf-8")
        for check in (
            "Resolve-DnsName",
            "Invoke-WebRequest",
            "'-Pn', '-p-', '--open', '-oX', '-'",
            "login route rate limit enforced",
            "request body size limit enforced",
            "Test-Tcp",
            "X-Auth-Request-Subject",
            "stock-ai-test",
            "cookie",
            "Set-Content -LiteralPath $EvidencePath",
            "failed_stage = $failedStage",
            "app_dns = [ordered]@{ a = $appIPv4; aaaa = $appIPv6 }",
            "issuer_dns = [ordered]@{ a = $idpIPv4; aaaa = $idpIPv6 }",
            "tls_chain_and_hostname_validated = $true",
            "Account bearer reaches Backend Account validation",
            "public edge health route",
            "anonymous API route",
            "unrecognized Host header is rejected",
        ):
            self.assertIn(check, script)
        self.assertIn("spoof_values_recorded = $false", script)
        self.assertNotIn("Set-Cookie", script.split("$report =", 1)[-1])

    def test_external_probe_validates_issuer_and_records_per_address_tls_metadata(self):
        script = EXTERNAL_CHECK.read_text(encoding="utf-8")
        for required in (
            "function Get-TlsMetadata",
            "$stream.AuthenticateAsClient($HostName)",
            "-Address $address -Port 443",
            "certificate_issuer = $certificate.Issuer",
            "not_after_utc = $certificate.NotAfter.ToUniversalTime().ToString('o')",
            "app_tls_certificates = $appTlsCertificates",
            "issuer_tls_certificates = $idpTlsCertificates",
            "Get-OpenTcpPorts -Address $address -IPv6 $isIPv6",
            "Test-Tcp -Address $address -Port $port",
        ):
            self.assertIn(required, script)
        self.assertNotIn("Get-OpenTcpPorts -HostName", script)

    def test_powershell_hosts_and_issuer_are_stable_dns_on_port_443(self):
        script = EXTERNAL_CHECK.read_text(encoding="utf-8")
        helper = script.split("function Test-StableDnsHostname", 1)[1].split("$issuerUri =", 1)[0]
        for required in (
            "[System.Net.IPAddress]::TryParse",
            "$normalized.EndsWith('..')",
            "$normalized -eq 'localhost'",
            "$normalized -eq 'loopback'",
            "$normalized.EndsWith('.localhost')",
            "$normalized.EndsWith('.loopback')",
            "$normalized.EndsWith('.local')",
            "$labels.Count -lt 2",
        ):
            self.assertIn(required, helper)
        self.assertIn("$issuerUriPort -ne 443", script)
        self.assertIn("Test-StableDnsHostname $issuerHost", script)
        self.assertIn("Test-StableDnsHostname $AppHost", script)
        self.assertIn("Get-TlsMetadata -HostName $issuerUri.Host -Address $address -Port 443", script)


if __name__ == "__main__":
    unittest.main()
