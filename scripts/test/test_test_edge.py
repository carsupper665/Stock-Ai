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
        self.assertNotIn("ports", self.services["oidc-gateway"])

    def test_gateway_uses_isolated_test_client_and_exact_https_urls(self):
        env = self.services["oidc-gateway"]["environment"]
        self.assertEqual(env["STOCK_AI_ENVIRONMENT"], "Test")
        self.assertEqual(env["OIDC_CLIENT_ID"], "stock-ai-test")
        self.assertEqual(env["OIDC_COOKIE_NAME"], "__Host-stock-ai-test-session")
        self.assertTrue(env["OIDC_ISSUER"].startswith("${OIDC_ISSUER"))
        self.assertTrue(env["OIDC_JWKS_URL"].startswith("${OIDC_JWKS_URL"))
        self.assertTrue(env["OIDC_REDIRECT_URI"].startswith("https://${TEST_APP_HOST"))
        self.assertEqual(
            {secret["target"] for secret in self.services["oidc-gateway"]["secrets"]},
            {"stock-ai-test_oidc_client_secret", "stock-ai-test_oidc_cookie_key"},
        )
        secret_sources = {secret["source"] for secret in self.services["oidc-gateway"]["secrets"]}
        self.assertEqual(secret_sources, set(self.compose["secrets"]))
        self.assertEqual(
            set(self.compose["secrets"]),
            {"test_oidc_client_secret", "test_oidc_cookie_key"},
        )
        self.assertEqual(
            set(self.services["nginx"]["depends_on"]),
            {"console", "oidc-gateway"},
        )
        for dependency in self.services["nginx"]["depends_on"].values():
            self.assertEqual(dependency["condition"], "service_healthy")

    def test_forwarded_and_identity_headers_are_replaced_at_each_proxy_boundary(self):
        for header in (
            "X-Real-IP $remote_addr",
            "X-Forwarded-For $remote_addr",
            "X-Forwarded-Proto https",
            "X-Forwarded-Host ${TEST_APP_HOST}",
            'Authorization ""',
            'X-Auth-Request-Subject ""',
            'X-Auth-Request-Access-Token ""',
            'X-Forwarded-Port ""',
            'X-Forwarded-Server ""',
            'X-Forwarded-Prefix ""',
            'Forwarded ""',
            'X-Original-URL ""',
            'X-Original-URI ""',
        ):
            self.assertIn(f"proxy_set_header {header};", self.nginx)
        self.assertIn("auth_request /_oauth2_auth;", self.nginx)
        self.assertIn("auth_request /_v1_auth;", self.nginx)
        self.assertIn("internal;", self.nginx)
        self.assertIn("location = /oauth2/auth", self.nginx)
        self.assertRegex(self.nginx, r"location = /oauth2/auth\s*\{\s*return 404;")
        self.assertIn("proxy_pass_request_body off;", self.nginx)
        self.assertGreaterEqual(self.nginx.count("proxy_pass_request_headers off;"), 5)
        self.assertIn("map $uri $console_authorization", NGINX_MAIN.read_text(encoding="utf-8"))
        self.assertIn("~^/v1(?:/|$) $http_authorization;", NGINX_MAIN.read_text(encoding="utf-8"))
        self.assertIn("proxy_set_header Authorization $console_authorization;", self.nginx)
        self.assertIn("proxy_set_header X-Auth-Request-Access-Token $oidc_access_token;", self.nginx)
        self.assertRegex(self.nginx, r"location = /_v1_auth\s*\{\s*internal;\s*if \(\$http_authorization != \"\"\) \{ return 204; \}")
        self.assertIn('location = /healthz', self.nginx)
        self.assertIn('location ^~ /assets/', self.nginx)
        self.assertIn('location ~ ^/v1(?:/|$)', self.nginx)
        self.assertIn('location ^~ /agent/', self.nginx)
        self.assertIn('location ^~ /llm/', self.nginx)
        self.assertIn('if ($http_authorization != "") { return 403; }', self.nginx)
        self.assertNotIn("$v1_auth_subrequest", NGINX_MAIN.read_text(encoding="utf-8"))
        console_location = self.nginx.split("location / {", 1)[1]
        self.assertNotIn("proxy_set_header Cookie", console_location)
        for header in ("Subject", "User", "Email"):
            self.assertRegex(
                self.nginx,
                rf"proxy_set_header X-Auth-Request-{header} \$oidc_{header.lower()};",
            )

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

    def test_live_probe_covers_matrix_route_classes_and_role_rejection(self):
        script = EXTERNAL_CHECK.read_text(encoding="utf-8")
        gateway_dockerfile = (ROOT / "docker/oidc-gateway.Dockerfile").read_text(encoding="utf-8")
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
        self.assertIn("oidc_gateway/internal/**/*_test.go", DOCKERIGNORE.read_text(encoding="utf-8"))
        self.assertIn("!scripts/issuer-check/**/*.go", DOCKERIGNORE.read_text(encoding="utf-8"))
        self.assertIn("COPY oidc_gateway/ ./", gateway_dockerfile)
        self.assertIn("COPY scripts/issuer-check/", gateway_dockerfile)

    def test_http_redirect_and_auth_routes_preserve_browser_contract(self):
        self.assertIn("return 301 https://${TEST_APP_HOST}$request_uri;", self.nginx)
        self.assertEqual(self.nginx.count("if ($host != ${TEST_APP_HOST}) { return 421; }"), 2)
        self.assertNotIn("proxy_set_header Host $host;", self.nginx)
        self.assertNotIn("proxy_set_header X-Forwarded-Host $host;", self.nginx)
        self.assertIn("location = /oauth2/callback", self.nginx)
        self.assertIn("location = /oauth2/start", self.nginx)
        self.assertIn("location ^~ /oauth2/", self.nginx)
        self.assertIn("return 302 /oauth2/start;", self.nginx)
        self.assertIn("server_name ${TEST_APP_HOST};", self.nginx)

    def test_operator_instructions_separate_static_review_from_live_acceptance(self):
        doc = GATEWAY_DOC.read_text(encoding="utf-8").lower()
        for required in (
            "docker compose",
            "stock-ai-test",
            "stock-ai-test_oidc_client_secret",
            "stock-ai-test_oidc_cookie_key",
            "issuer-check",
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
            "passed: scripts.test.test_test_edge (9 tests)",
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
            "Account bearer reaches Console Account validation",
            "public edge health route",
            "anonymous API route",
            "unrecognized Host header is rejected",
        ):
            self.assertIn(check, script)
        self.assertIn("spoof_values_recorded = $false", script)
        self.assertNotIn("Set-Cookie", script.split("$report =", 1)[-1])


if __name__ == "__main__":
    unittest.main()
