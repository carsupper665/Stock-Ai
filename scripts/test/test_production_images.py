#!/usr/bin/env python3
import pathlib
import re
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[2]
DOCKERFILES = {
    "backend": ROOT / "docker/backend.Dockerfile",
    "agent-server": ROOT / "docker/agent-server.Dockerfile",
    "llm-provider-server": ROOT / "docker/llm-provider-server.Dockerfile",
    "middleware": ROOT / "docker/middleware.Dockerfile",
    "nginx": ROOT / "docker/nginx.Dockerfile",
}
SCRATCH_IMAGES = ("backend", "agent-server", "llm-provider-server", "middleware")
NGINX_BASE = "nginx@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94"
GO_BASE = "golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079"
NODE_BASE = "node:24.21.0-alpine3.23@sha256:9ec4a2e289874ed0d722e1772ec2de45d2801541db8612f3638b26f128c69ac2"
TRIVY_IMAGE = "aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa"


class ProductionImagePolicyTest(unittest.TestCase):
    def test_four_scratch_runtimes_and_one_pinned_nginx_runtime(self):
        self.assertEqual(set(DOCKERFILES), {"backend", "agent-server", "llm-provider-server", "middleware", "nginx"})
        for name, path in DOCKERFILES.items():
            text = path.read_text(encoding="utf-8")
            stages = re.findall(r"^FROM (.+)$", text, re.MULTILINE)
            self.assertGreaterEqual(len(stages), 2, name)
            for stage in stages[:-1]:
                self.assertRegex(stage, r"@sha256:[0-9a-f]{64}(?: AS .+)?$", name)
            if name == "nginx":
                self.assertEqual(stages[-1], NGINX_BASE)
                self.assertIn(NODE_BASE, text)
                self.assertIn("/usr/share/nginx/html", text)
                continue
            self.assertEqual(stages[-1], "scratch", name)
            self.assertIn(GO_BASE, text, name)
            self.assertIn("-tags=timetzdata", text, name)
        self.assertIn("COPY middleware/ ./", DOCKERFILES["middleware"].read_text(encoding="utf-8"))

    def test_runtime_contract_is_non_root_traceable_and_healthy(self):
        required = (
            "USER 65532:65532",
            "HEALTHCHECK ",
            "org.opencontainers.image.created",
            "org.opencontainers.image.revision",
            "org.opencontainers.image.source",
            "org.opencontainers.image.version",
            'ENTRYPOINT ["/',
        )
        for name in SCRATCH_IMAGES:
            runtime = DOCKERFILES[name].read_text(encoding="utf-8").split("\nFROM scratch\n", 1)[1]
            for value in required:
                self.assertIn(value, runtime, f"{name}: {value}")
            self.assertNotRegex(runtime, r"\b(RUN|ADD)\b", name)
            self.assertNotRegex(runtime, r"\b(apk|apt|curl|wget|busybox|sh)\b", name)
            self.assertNotIn("COPY .", runtime, name)
        nginx = DOCKERFILES["nginx"].read_text(encoding="utf-8").split(f"\nFROM {NGINX_BASE}\n", 1)[1]
        for value in required[2:6]:
            self.assertIn(value, nginx, f"nginx: {value}")
        self.assertNotIn("COPY .", nginx)
        for name in ("backend", "agent-server", "llm-provider-server"):
            self.assertIn("/data", DOCKERFILES[name].read_text(encoding="utf-8"), name)

    def test_build_pipeline_enforces_evidence_and_clean_source(self):
        text = (ROOT / "scripts/build-production-images.ps1").read_text(encoding="utf-8")
        for name in DOCKERFILES:
            self.assertIn(f"Name = '{name}'", text)
        for control in (
            "--provenance=mode=max",
            "--sbom=true",
            "--driver docker-container",
            'type=oci,dest=$ociPath',
            TRIVY_IMAGE,
            "--format spdx-json",
            "--severity HIGH,CRITICAL --exit-code 1 --format sarif",
            "trivy_image = $trivyImage",
            "docker export",
            "forbidden runtime files",
            "--read-only",
            "--cap-drop",
            "no-new-privileges:true",
            "State.Health.Status",
            "ca-certificates.crt",
            "same-input rebuild differs",
            "oci_archive_sha256",
            "critical_high_findings = 0",
            "acceptance-summary.json",
            "source_tree",
            "inputs_sha256",
            "SHA256SUMS",
            "--untracked-files=all",
        ):
            self.assertIn(control, text)
        for removed in ("Name = 'console'", "Name = 'oidc-gateway'", "docker/console.Dockerfile", "docker/oidc-gateway.Dockerfile", "oidc_gateway/"):
            self.assertNotIn(removed, text)
        for source in (
            "docker/middleware.Dockerfile", "docker/nginx.Dockerfile", "middleware/go.mod",
            "middleware/cmd/middleware/main.go", "middleware/internal/gateway/proxy.go",
        ):
            self.assertIn(source, text)
        self.assertNotIn("docker scout", text.lower())
        self.assertNotIn("golang:1.27.1", "\n".join(path.read_text(encoding="utf-8") for path in DOCKERFILES.values()))

    def test_middleware_image_smoke_uses_ephemeral_test_file_secrets(self):
        build = (ROOT / "scripts/build-production-images.ps1").read_text(encoding="utf-8")
        middleware = DOCKERFILES["middleware"].read_text(encoding="utf-8")
        for value in (
            "stock-ai-test_oidc_client_secret",
            "stock-ai-test_oidc_cookie_key",
            "stock-ai-test_agent_admin_token",
            "stock-ai-test_llm_admin_token",
            "stock-ai-test_llm_runtime_token",
            "stock-ai-test_backend_middleware_token",
            "AGENT_SERVER_ADMIN_TOKEN_FILE = '/run/secrets/stock-ai-test_agent_admin_token'",
            "LLM_SERVER_ADMIN_TOKEN_FILE = '/run/secrets/stock-ai-test_llm_admin_token'",
            "LLM_SERVER_RUNTIME_TOKEN_FILE = '/run/secrets/stock-ai-test_llm_runtime_token'",
            "BACKEND_USER_TOKEN_FILE = '/run/secrets/stock-ai-test_backend_middleware_token'",
            "MIDDLEWARE_ADDR = ':4180'",
            "BACKEND_URL = 'http://backend.invalid'",
            "AGENT_SERVER_URL = 'http://agent.invalid'",
            "LLM_SERVER_URL = 'http://llm.invalid'",
            "OIDC_ISSUER = 'https://identity.invalid/realms/stock-ai-test'",
            "OIDC_CLIENT_ID = 'stock-ai-test'",
            "--mount",
            "readonly",
            "Remove-Item -Recurse -Force -LiteralPath $runtimeSecretsDirectory",
        ):
            self.assertIn(value, build)
        self.assertNotIn("OIDC_GATEWAY_ADDR", build)
        self.assertIn("/run/secrets", middleware)
        self.assertIn('ENTRYPOINT ["/middleware"]', middleware)
        self.assertNotIn("issuer-check", middleware)
        self.assertIn("/healthz", middleware)

    def test_nginx_image_is_built_and_checked_like_the_others_but_with_its_own_runtime(self):
        build = (ROOT / "scripts/build-production-images.ps1").read_text(encoding="utf-8")
        self.assertIn("Name = 'nginx'; Dockerfile = 'docker/nginx.Dockerfile'", build)
        self.assertIn("usr/share/nginx/html/index.html", build)
        self.assertIn("$isNginx = $name -eq 'nginx'", build)
        self.assertIn("User = '101:101'", build)

    def test_context_excludes_secrets_tests_and_local_state(self):
        patterns = (ROOT / ".dockerignore").read_text(encoding="utf-8").splitlines()
        self.assertEqual(patterns[0], "**")
        allowed = "\n".join(line for line in patterns if line.startswith("!"))
        for forbidden in (".env", ".db", ".log", "/test", ".scratch"):
            self.assertNotIn(forbidden, allowed)


if __name__ == "__main__":
    unittest.main()
