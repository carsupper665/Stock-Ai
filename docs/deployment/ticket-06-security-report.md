# Ticket 06 security gate report

Date: 2026-10-12 (Asia/Taipei)  
Decision: **PASS — accepted for containerization**  
Accepted application revision: `9d5da8350c43c25fdea7ef836fdfe46f186c5256`

This decision covers the Backend, Agent Server, LLM Provider Server, OIDC
Gateway, frontend, deployment security contract, and the source tree at the
revision above. The later evidence-only commit that adds this report and its
artifacts does not change the accepted application source.

## Reproduction

Run from a clean checkout of the accepted revision. The gate rejects tracked,
staged, or untracked changes both before and after execution.

```text
mkdir -p /tmp/stock-ai-security-gate-bin
GOBIN=/tmp/stock-ai-security-gate-bin go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
GOBIN=/tmp/stock-ai-security-gate-bin go install github.com/zricethezav/gitleaks/v8@v8.24.2
GOTOOLCHAIN=go1.27.2 \
  GOVULNCHECK=/tmp/stock-ai-security-gate-bin/govulncheck \
  GITLEAKS=/tmp/stock-ai-security-gate-bin/gitleaks \
  scripts/run-security-gate.sh /tmp/stock-ai-security-gate-9d5da83-final-v2
```

The script executes these controls:

```text
for each of backend agent_server llm_provider_server oidc_gateway:
  go test -race -count=1 -json ./...
  go vet ./...
  govulncheck -json ./...
python3 scripts/validate_security_contract.py
cd frontend && npm ci --ignore-scripts
cd frontend && npm test -- --reporter=json --outputFile=<artifact>/frontend-tests.json
cd frontend && npm run build
cd frontend && npm audit --json
gitleaks dir --no-banner --redact=100 --report-format json --report-path <artifact>/gitleaks.json .
```

Versions recorded by the run were Linux 6.18.33.2 WSL2, Git 2.43.0, Go
1.27.2 linux/amd64, Node 24.21.0, npm 11.19.0, govulncheck 1.9.0 with the
2026-10-08 vulnerability database, and gitleaks v8.24.2. The gitleaks binary
SHA-256 was
`35ac0466b17b82c959fcb96651fbdd4ea8c311063a8626efe3f0e68eb87fbd59`.
See `security-gate/9d5da83/metadata.json` for the machine-readable record.

## Results

| Control | Result |
|---|---|
| Backend Go race suite | PASS: 334 tests; 11 packages; 1 environment-dependent test skipped |
| Agent Server Go race suite | PASS: 405 tests; 2 packages; no test skipped |
| LLM Provider Server Go race suite | PASS: 250 tests; 2 packages; no test skipped |
| OIDC Gateway Go race suite | PASS: 43 tests; 1 package; no test skipped |
| Go vet | PASS: all four logs empty |
| Frontend | PASS: 54/54 tests in 11/11 suites; production build transformed 82 modules |
| Security contract | PASS: 31 authorization cases and 14 risks validated |
| Source secrets | PASS: gitleaks reported 0 findings; reports are 100% redacted |
| JavaScript dependencies | PASS: npm audited 103 packages; 0 Critical, High, Moderate, Low, or Info findings |
| Go vulnerabilities | PASS under the policy below: no callable finding and no unapproved finding |

The sole skipped test was
`TestTicket23PostgresMutationsAreSerializedPerAccount`: this host did not set
`BACKEND_TEST_POSTGRES_DSN`, so its external-PostgreSQL row-locking behavior was
not re-exercised. No Ticket 06 security test was skipped.

## Security behavior and disclosure audit

- The executable authorization matrix and Backend/Console boundary tests cover
  anonymous, wrong-role, wrong-audience, cross-account, spoofed-header, and
  internal-service credential cases.
- `TestConsoleServesOnlySafePublicPaths` covers dot-segment, percent-encoded,
  backslash traversal, missing assets, and browser security headers.
- `TestSecurityGateRejectsOversizedAgentRequest` and
  `TestSecurityGateRejectsOversizedProviderRequest` cover request-size limits.
- `TestCallbackIsOneTimeAndAllFailuresStayUnauthenticated` covers state reuse,
  failed nonce/PKCE/exchange, IdP denial, and login replay. OIDC Gateway and
  Console tests verify cache, content-type, referrer, and related response
  headers.
- `TestAccessLogExcludesQueryValuesAndCredentials` proves access tokens, client
  secrets, internal service credentials, and Provider tokens do not enter
  Backend access logs. Database and Agent operational-log tests likewise reject
  Account tokens and gateway details.
- Console disclosure tests inspect status, headers, redirects, decoded body,
  and trailers for its three internal credentials. OIDC response tests reject
  access-token/client-secret disclosure.
- Provider and harness diagnostic tests reject Provider tokens, prompts, raw
  stderr, and upstream credential text in public responses and logs. Agent
  diagnostic/history tests preserve bounded audit data without secrets; history
  downloads and browser-facing responses are covered by the full suites.
- Production config tests keep master keys and service credentials in the
  required file-backed secret boundary. The source scan includes ignored-file
  policy and permits only four exact, deterministic test-fixture fingerprints
  in `.gitleaksignore`.

## Findings and exception policy

The initial gate found one High JavaScript dependency advisory in
`source-map-js`; the accepted revision upgrades it from 1.2.1 to 1.2.2. Go
dependencies and the Go directive were upgraded until govulncheck found no
application call path to a vulnerability. Therefore there is no unresolved
Critical or High finding.

One lower-severity exception remains:

| Advisory | Severity | Owner | Compensating control | Expiry |
|---|---|---|---|---|
| GO-2026-5932 | Informational/imported-only | Stock AI release owner | The vulnerable `openpgp` package is neither imported nor called by application code; the gate fails immediately if a project call trace appears. | 2027-01-31 |

The policy summary is archived in
`security-gate/9d5da83/govulncheck-summary.json`; raw govulncheck streams retain
the complete trace evidence.

## Residual risks and progression

- The external PostgreSQL serialization check requires a configured database
  and remains an environment-certification item rather than an application
  security exception.
- Container/image provenance, Compose isolation, real Test IdP/TLS/DNS,
  Windows-host behavior, promotion, backup/restore, and Release acceptance are
  outside this source gate and remain blocked on Tickets 07–16.
- A future vulnerability database or lockfile change can invalidate this
  decision. Containerization must use this accepted revision (or rerun the gate
  for a different revision) and preserve the archived scan evidence.

All raw test and scan outputs are under `security-gate/9d5da83/`. Go test and
govulncheck streams are newline-delimited JSON compressed with deterministic
gzip; `SHA256SUMS` verifies every archived artifact.

## Sign-off

Signed off by: AgentCraft Kit, pre-containerization security gate executor  
Control owner: Stock AI release owner  
Signed at: 2026-10-12 (Asia/Taipei)  
Decision: revision `9d5da8350c43c25fdea7ef836fdfe46f186c5256` may progress to Ticket 07.
