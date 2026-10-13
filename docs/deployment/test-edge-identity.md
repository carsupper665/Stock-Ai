# Test edge and identity stack

This overlay adds the Test OIDC Gateway and Nginx to the Ticket 08 application
Compose project. Nginx alone publishes host ports 80 and 443; Console, the
Gateway, Backend, Agent Server, and LLM Provider Server have no host ports.
The Test IdP is an independently administered Test-only realm/tenant on the
stable HTTPS issuer URL. Use a dedicated Test IdP instance if that is how the
Identity Owner provisions environments. Do not connect this deployment to
Release or production accounts, keys, sessions, clients, or secrets.

## Host and identity contract

Choose stable DNS names, for example app-test.example and idp-test.example;
replace these examples with the approved Test names in the machine-local
Compose env file. The issuer must remain the exact same HTTPS URL from browsers,
the Gateway, Console, and Backend, for example
https://idp-test.example/realms/stock-ai-test. Docker DNS, localhost, and
host-specific aliases must never replace the issuer claim or JWKS URL.

Register only the confidential stock-ai-test client in the Test realm:

- Redirect URI: https://<TEST_APP_HOST>/oauth2/callback
- Post-logout URI: https://<TEST_APP_HOST>/
- Authorization Code flow enabled; implicit and password grants disabled.
- Access token audience and stable subject must match the Test app's
  OIDC_AUDIENCE and OIDC_USER_SUBJECT values.
- Use a Test-only account, client secret, signing keys, session store, and
  recovery process. Keep administrator access private to the Identity Owner.

The Test certificate mounted at TEST_TLS_DIR must have a complete chain in
fullchain.pem and its matching private key in privkey.pem. The certificate SAN
must cover TEST_APP_HOST; the issuer hostname must independently present a
browser-trusted certificate. Nginx rejects unexpected Host values with 421 and
uses the configured app hostname in redirects and upstream Host headers. Do
not copy a private key into this repository.

Nginx disables forwarding of inbound request headers at every upstream
boundary, then sets a small allowlist with connection-derived forwarding
values. It sends cookies only to the Gateway and its internal auth subrequest.
The Console receives the verified Gateway access token in the trusted
X-Auth-Request-Access-Token header, never in Authorization. The public
/healthz and /assets/* paths do not invoke OIDC. Browser UI routes redirect
unauthenticated users to login. API routes return 401 without a session. An
explicit Authorization value on /v1 bypasses the Gateway session subrequest
and reaches Console's independent Account-token validation; its opaque Account
Bearer is preserved only for /v1. /agent/* and /llm/* reject any browser
Authorization value with 403, then use the validated OIDC identity from the
trusted Gateway headers. The public /oauth2/auth path is not exposed; only the
Nginx internal auth subrequest can reach it.

## Machine-local configuration

Create an env file outside Git from deploy/compose/compose.env.example, then
add the values below. Set OIDC_GATEWAY_IMAGE to the versioned image produced by
the traceable image pipeline. Replace the NGINX digest placeholder with the
approved official-image manifest digest. Both must identify reviewed images,
not floating tags.

    OIDC_GATEWAY_IMAGE=stock-ai/oidc-gateway:0.7.0
    NGINX_IMAGE=nginx:1.27-alpine@sha256:replace-with-reviewed-manifest-digest
    TEST_APP_HOST=app-test.example
    TEST_BIND_ADDRESS=0.0.0.0
    TEST_TLS_DIR=C:/ProgramData/StockAI/Test/tls
    OIDC_ISSUER=https://idp-test.example/realms/stock-ai-test
    OIDC_AUTHORIZATION_URL=https://idp-test.example/realms/stock-ai-test/protocol/openid-connect/auth
    OIDC_TOKEN_URL=https://idp-test.example/realms/stock-ai-test/protocol/openid-connect/token
    OIDC_JWKS_URL=https://idp-test.example/realms/stock-ai-test/protocol/openid-connect/certs
    OIDC_END_SESSION_URL=https://idp-test.example/realms/stock-ai-test/protocol/openid-connect/logout
    OIDC_USER_SUBJECT=replace-with-approved-test-subject

Use STOCK_AI_SECRETS_DIR from the base env file for two Test-only files:

- stock-ai-test_oidc_client_secret: confidential client secret from the Test
  IdP registration.
- stock-ai-test_oidc_cookie_key: canonical base64 encoding of 32 random bytes.

Restrict the Windows directory ACL to the deployment identity and approved
operators. Do not paste secret values into a shell command, evidence report, or
Git. Use deploy/compose/compose.yaml and
deploy/compose/compose.test-edge.yaml together with the same local env file:

    docker compose --env-file C:\ProgramData\StockAI\Test\compose.env -f deploy/compose/compose.yaml -f deploy/compose/compose.test-edge.yaml config --quiet
    docker compose --env-file C:\ProgramData\StockAI\Test\compose.env -f deploy/compose/compose.yaml -f deploy/compose/compose.test-edge.yaml up -d

The base compose file passes the same exact issuer and JWKS URL to the
application's OIDC validators. Confirm the app hostname and redirect/logout
URIs exactly match the Test client registration before starting containers.

## Checks and evidence

Run static policy tests from the repository root:

    python -m unittest scripts.test.test_test_edge

After deployment, prove DNS, certificate trust, exact issuer discovery, and
JWKS reachability from the private Compose network, not merely from the Windows
host. The Gateway image includes an issuer-check command for this purpose:

    docker compose --env-file C:\ProgramData\StockAI\Test\compose.env -f deploy/compose/compose.yaml -f deploy/compose/compose.test-edge.yaml exec -T oidc-gateway /issuer-check Test https://idp-test.example/realms/stock-ai-test

Use the actual configured issuer in the command. It fails on DNS/TLS/HTTP
errors, issuer mismatch, a JWKS URL outside the realm, or an empty key set. A
successful output records the exact issuer, JWKS host, and key count without
recording key material.

From a network outside the Test computer, verify the A/AAAA records, the
certificate chain and hostname, HTTP-to-HTTPS redirect, required response
headers, /oauth2/start redirect to the exact issuer, successful Test login
and logout, and whether ports 80/443 alone are reachable. Run the external
probe from that independent network (PowerShell 7):

    pwsh -NoProfile -File ./scripts/check-test-edge.ps1 -AppHost app-test.example -Issuer https://idp-test.example/realms/stock-ai-test -EvidencePath C:/ProgramData/StockAI/Test/evidence/test-edge-external.json -FullPortScan

The independent probe needs Nmap installed. The script checks DNS,
browser-trusted HTTPS, redirect/client/cookie configuration, response headers,
anonymous assets and health, 401 behavior on anonymous API routes, Account
Bearer routing to Console, rejection of Account Bearers on Agent/LLM routes,
spoofed-header rejection, request-size and login-rate limits, IdP discovery and
JWKS, expected public ports, known private ports, and all TCP ports with Nmap.
It writes sanitized results
before returning a failure status. Complete an interactive Test login and
logout separately and record that outcome without retaining browser cookies.
Explicitly probe Gateway and application ports such as 4180, 8088, 7794, 8080,
and 8090 and confirm they are closed. The script's full TCP scan expects only
ports 80 and 443 to be open on the app hostname. Record timestamp, probe
vantage, DNS answers, tested hostnames/ports, TLS issuer/expiry, status codes,
and pass/fail results in a sanitized JSON file under the Test host's evidence directory. Exclude tokens,
cookies, client secrets, private keys, user PII, and IdP response bodies.

Do not mark live external acceptance complete until the checks have actually
run from the Test host and an independent network. If DNS, IdP registration,
certificate, Test host, or external probe access is not available, write
not run with the specific dependency in the evidence summary; static policy
tests do not establish runtime or external reachability.

The current sanitized status record is
`deploy/evidence/test-edge-identity.not-run.json`. It records local checks
separately from Test-host and external checks that have not run. Replace or
supersede it with actual sanitized Test-host evidence after provisioning; do
not change a `not_run` result based on configuration review alone.
