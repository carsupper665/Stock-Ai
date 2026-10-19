# Stock AI deployment security contract

**Contract ID:** `SEC-DEPLOY-01`  
**Version:** 1.0  
**Status:** normative; changes require Security Owner review  
**Executable cases:** `docs/deployment/authorization-matrix.csv`  
**Validation:** `python3 scripts/validate_security_contract.py`

This is the single security contract for the Windows OIDC Docker release. The
words **MUST**, **MUST NOT**, **SHALL**, and **SHALL NOT** are release gates.
Test and Release are separate security domains. A value, identity, key, session,
database, volume, certificate, or image is never assumed trustworthy merely
because the equivalent object is trusted in the other domain.

## 1. Network listeners and trust boundaries

| Component | Bind/listener | Reachable from | Trust and rule |
|---|---|---|---|
| Nginx edge | host TCP 443; TCP 80 redirect only | approved client networks | The only public application listener. Terminates TLS, applies limits and security headers, serves the static frontend files directly, and forwards only `/oauth2`, `/v1`, `/agent` and `/llm` to Middleware. It performs no authentication, makes no authentication subrequest, and never receives an access token. |
| Middleware | container TCP 4180, no host publication | Nginx on the environment's edge network | The only login and authentication layer. Confidential OIDC client that owns login state, nonce, PKCE verifier, the application session cookie and the server-side access token. Only its own validated session is an authenticated browser identity; it is the only upstream reachable by Nginx and the only holder of the private-service credentials. |
| Backend | container TCP 7794, no host publication | Middleware and Agent Server on the private application network | Backend does not validate OIDC. It authenticates Middleware by the `backend_middleware_token` USER credential, or an Account token passed through unchanged. It never trusts identity headers. `/v1/health` is private to container health checks. |
| Agent Server | container TCP 8080, no host publication | Middleware on the private application network | Requires its Admin credential for `/api/v1/*`; uses a dedicated Backend USER credential and LLM Runtime credential for outbound calls. |
| LLM Provider Server | container TCP 8090, no host publication | Middleware and Agent Server on the private application network | Admin credential controls configuration; Runtime credential permits model discovery/generation only. Provider secrets remain server-side. |
| IdP | stable environment-specific HTTPS TCP 443 | browsers and Middleware | Separate Test realm/tenant and Release realm/tenant. The externally stable issuer string is used everywhere; no `localhost` issuer alias. |
| External model/market providers | outbound HTTPS TCP 443 only | LLM Provider Server or Backend, as explicitly configured | Untrusted responses. TLS validation, timeouts, response-size limits and secret-safe errors are required. No inbound trust is granted. |
| Container registry | outbound HTTPS TCP 443 during pull/promotion only | Test/Release Docker engine through the operator workflow | Digest-addressed read access. Release receives no source/build context and no push credential. |
| DNS, time and trust services | host-managed DNS/NTP and HTTPS certificate validation | Windows/Docker host and containers as required | Approved infrastructure only. Correct time and DNS are authentication dependencies; failure is fail-closed, never an issuer-hostname rewrite. |
| Persistent data | no network listener | only the service container named in section 3 | Environment-local volumes. No volume is mounted into Nginx or Middleware unless explicitly listed below. |

The boundaries are: (1) browser/untrusted network → Nginx, (2) Nginx → Middleware
edge network, (3) Middleware → private application network,
(4) application → environment-local persistent volumes, (5) application → IdP,
and (6) application → external providers. Network location is never an identity.
TLS is mandatory across boundary 1 and for boundaries 5–6. The isolated Docker
networks are the compensating transport control for boundaries 2–4; no private
listener may be published on a host interface.

## 2. Identities and permissions

| Identity | Authentication | Permissions | Explicitly forbidden |
|---|---|---|---|
| Anonymous browser | none | Public static assets, edge liveness, login start and one-time callback | `/v1` data, `/agent`, `/llm`, internal health endpoints, or any credential-bearing response |
| OIDC USER | Middleware session backed by a valid environment-local Authorization Code flow; Backend sees only the Middleware credential | USER-level Backend APIs; Middleware may substitute Agent Admin, LLM Admin, or LLM Runtime credentials only for the matrix routes | Supplying or reading private-service credentials; using Test identity in Release or vice versa |
| Account token | Opaque Backend token in `Authorization: Bearer`; validated only by Backend | Account self-service, market reads, own trading and own messages as implemented by Backend | USER account administration, Agent/LLM routes, or conversion/fallback to OIDC USER |
| Middleware | Private workload plus its own verified OIDC USER session | Route to Backend with `backend_middleware_token`; replace browser credentials with the one exact service credential required by an allowed `/agent` or `/llm` route | Trust browser identity headers, expose credentials, or call persistent stores directly |
| Agent Server | Agent Admin for inbound; dedicated Backend USER and LLM Runtime credentials outbound | Agent administration/runtime; Backend and model operations required for agent runs | LLM administration, browser session creation, or accepting Account tokens as Admin |
| LLM Provider Server | LLM Admin or LLM Runtime bearer, selected by endpoint | Admin: provider/token/catalog management. Runtime: model list and generation only | Returning Provider tokens/master key or accepting one credential in the other's role |
| Operator | Windows administrator plus documented deployment authorization | Manage configuration, Docker, certificates, backup/restore and promotion | Reusing Test secrets in Release, rebuilding on Release, or bypassing recorded gates |

Authentication is fail-closed. Only Middleware validates OIDC; Backend does not
validate OIDC or JWTs, and an invalid or missing Middleware session MUST NOT fall
back to the USER credential or an Account identity. The browser never holds or
supplies the `backend_middleware_token`. Account-token requests have separate
semantics: Nginx may pass an opaque Bearer request only to `/v1`; Middleware
preserves it unchanged; Backend alone resolves it. An Account bearer never
unlocks `/agent` or `/llm`.

## 3. Credentials, persistent assets, and dependencies

| Item | Owner/consumer | Storage and handling | Isolation/rotation |
|---|---|---|---|
| TLS private key/certificate | Edge Operator / Nginx | Windows-protected secret mount; private key never in image, Git, log or browser | Unique per environment/hostname; renew in place and test before activation |
| OIDC client secret | Identity Owner / Middleware | Environment secret namespace, server-side only | Independent confidential clients; rotate at IdP and Middleware together |
| OIDC cookie encryption/signing key | Security Owner / Middleware | Environment secret namespace | Independent random key and cookie name; rotation invalidates only that environment's sessions |
| IdP signing private key | Identity Owner / IdP only | IdP-protected key store; only public JWKS leaves the IdP | Independent per realm/tenant; Release private key never enters Test |
| Allowed USER subjects | Identity Owner / IdP, Middleware | Environment configuration containing stable subject identifiers, not display names | Separate Test and Release allowlists; reviewed as authorization data |
| Backend middleware credential | Security Owner / Middleware, Backend | Secret mount `stock-ai-<environment>_backend_middleware_token`; Backend accepts it as its USER credential | Unique per environment and distinct from every other credential, including the Agent→Backend USER credential; rotate Middleware and Backend atomically |
| Account tokens | Backend / account holder | AES-GCM ciphertext plus a database-unique keyed lookup in the Backend database; plaintext exists only at issuance/authorized display/request validation; never logs or browser storage | Unique per account; duplicate create/rotation fails at the database; reset revokes prior token. The environment-specific encryption key is preserved with protected backups |
| Backend Account-token encryption key | Security Owner / Backend only | Environment secret namespace; mounted read-only and never stored with the Backend database | Unique per environment; preserve with retained database backups; rotation requires decrypt-and-re-encrypt |
| Agent Admin credential | Security Owner / Middleware, Agent | Secret mount, never frontend environment/build args | Unique per environment; rotate Middleware and Agent atomically |
| Agent→Backend USER credential | Security Owner / Agent, Backend | Secret mount | Distinct from browser/session credentials and unique per environment |
| LLM Admin credential | Security Owner / Middleware, LLM | Secret mount | Unique per environment; never usable as Runtime by policy |
| LLM Runtime credential | Security Owner / Middleware, Agent, LLM | Secret mount | Unique per environment; least privilege; rotate all consumers together |
| LLM master key | Security Owner / LLM | Secret mount separate from Provider database | Unique per environment; MUST be preserved with its encrypted DB; rotation requires an explicit re-encryption procedure |
| Provider API tokens | Application Owner / LLM | AES-GCM encrypted in LLM DB; decrypted only for provider call | Test uses non-production provider accounts/quotas; revoke on disclosure |
| Backend DB | Data Owner / Backend | Environment-specific volume; contains business data and Account tokens | Never cross-mounted; encrypted/ACL-protected backups under Ticket 13 |
| Agent DB and History | Data Owner / Agent | Environment-specific volumes | Never cross-mounted; backed up consistently with Backend state |
| LLM Provider DB | Application Owner / LLM | Environment-specific volume; paired with its master key | Never copied without its matching protected key and authorization |
| IdP database/key store | Identity Owner / IdP, when self-hosted | Dedicated environment-specific volume and protected backups | Never shared or cloned from Release into Test |
| Logs and backup sets | Operations Owner / producing services | Environment-specific bounded volumes/protected backup location | No secrets; retention and restore controls are Ticket 13 |
| Container images/SBOM/scan evidence | Release Owner | Registry artifacts addressed by immutable digest | Exact Test-approved digest promoted; Release never rebuilds |
| Registry pull credential | Release Owner / Docker engine | Destination-local credential store with pull-only scope | Different from Test/publisher credentials; rotate without changing image digest |
| IdP/JWKS | Identity Owner / Middleware | Outbound HTTPS using exact configured issuer | Cache is bounded and safe rotation is required; fail closed when unverifiable |
| Market/model APIs | Application Owner / Backend, LLM | Outbound HTTPS allowlist/configuration | Bounded connect/request timeout and response size; responses are untrusted |
| DNS/NTP/CA services | Platform Owner / host and containers | Approved infrastructure configuration | Monitored because name, time and certificate validation are OIDC/TLS dependencies |

No secret may be stored in Git, an image layer, Compose interpolation output,
frontend bundle, URL, JavaScript-readable storage/cookie, response, diagnostic,
or normal log. Secret files are read-only and accessible only to Windows
administrators and the consuming container.

## 4. Test and Release OIDC values

Values containing `${...}` are mandatory deployment inputs, not permission to
reuse a value. The configuration lint in Tickets 09 and 11 MUST resolve them to
real stable HTTPS FQDNs and prove every `MUST differ` constraint before startup.

| Value | Test | Release | Rule |
|---|---|---|---|
| Application hostname | `${TEST_APP_HOST}` | `${RELEASE_APP_HOST}` | Real distinct FQDNs; MUST differ |
| Issuer | `https://${TEST_IDP_HOST}/realms/stock-ai-test` | `https://${RELEASE_IDP_HOST}/realms/stock-ai-release` | Exact token `iss`; HTTPS; MUST differ; never localhost |
| Client registration / ID | `stock-ai-test` | `stock-ai-release` | Separate confidential clients and secrets |
| API audience | `stock-ai-api-test` | `stock-ai-api-release` | Exact audience allowlist; MUST differ |
| Redirect URI | `https://${TEST_APP_HOST}/oauth2/callback` | `https://${RELEASE_APP_HOST}/oauth2/callback` | Exact-match registration; no wildcard |
| Post-logout URI | `https://${TEST_APP_HOST}/` | `https://${RELEASE_APP_HOST}/` | Exact-match registration; no wildcard |
| Cookie name | `__Host-stock-ai-test-session` | `__Host-stock-ai-release-session` | MUST differ; `Secure; HttpOnly; SameSite=Lax; Path=/`; no Domain attribute |
| Client-secret owner | Test Identity Owner | Release Identity Owner | Different secret namespaces and access groups |
| Cookie-key owner | Test Security Owner | Release Security Owner | Generated independently on destination computer |
| Allowed subject policy | Test USER subject allowlist | Release USER subject allowlist | No shared users or subject identifiers |

Test SHALL use a separate IdP instance or an isolated realm/tenant. It MUST NOT
receive Release signing/private keys, client or cookie secrets, sessions, users,
subject allowlists, databases, Provider tokens, certificates, backups, or secret
files. Release public signing keys may be publicly discoverable through its HTTPS
JWKS endpoint; their public nature does not authorize copying any private signing
material. Test failures and resets must have no Release effect.

## 5. Trusted-header contract

At the public listener Nginx MUST clear, before any routing:

`Forwarded`, every `X-Forwarded-*`, `X-Real-IP`, `X-Original-URI`,
`X-Forwarded-Client-Cert`, `Proxy-Authorization`, `X-Remote-User`, `Remote-User`, `X-User`, `X-Email`,
`X-Auth-Request-User`, `X-Auth-Request-Email`, `X-Auth-Request-Preferred-Username`,
`X-Auth-Request-Subject`, and `X-Auth-Request-Access-Token`.

Nginx forwards to Middleware only an explicit allowlist of request headers and may
create only normalized `X-Forwarded-For`, `X-Forwarded-Proto=https`,
`X-Forwarded-Host`, `X-Real-IP`, and a validated/generated `X-Request-ID` from
connection data. Nginx performs no authentication: it never receives an access
token or any `X-Auth-Request-*` value from Middleware and never originates an
authenticated identity value.

`Authorization` is handled by route rather than trusted as an identity header.
Nginx removes it on authentication requests. It may preserve an opaque Bearer
value on `/v1`, where Middleware passes it unchanged and Backend validates it as
an Account token. On `/agent` and `/llm` any browser `Authorization` is rejected
by Middleware before an upstream is contacted. An explicit `/v1` Bearer always
selects Account delegation, even when the browser also has an OIDC session;
without that explicit Bearer, Middleware verifies its own session and creates the
Backend bearer from `backend_middleware_token`. JWT-shaped browser Bearers are
rejected. `Cookie` is sent only from Nginx to Middleware (the session boundary)
and is removed before Backend, Agent, or LLM proxying.

Middleware MUST verify its own session before any credential substitution. It
strips all authentication/forwarding headers before proxying to Backend, Agent or
LLM, then sets only the required Authorization credential. Backend never
consumes identity headers. Logs omit all authentication headers and cookies.
Nginx configuration tests and spoofing tests must prove these rules.

## 6. Executable authorization matrix

`docs/deployment/authorization-matrix.csv` is normative under this section. Each
row is a request that later integration tests MUST execute at Nginx. `edge_action`
is one of `allow`, `deny`, or `not_found` (Nginx only forwards or serves static
files); `middleware_action` records what Middleware does and may also be `deny`
or `not_found`; `upstream` is `none`, `middleware`, `backend`, `agent`, `llm`, or
`edge`. A denial by Nginx or Middleware with `upstream=none` MUST be proven not
to contact an application upstream. `/llm/v1/generate` is not open to browsers:
Middleware answers 404 and never reaches the LLM Provider Server.

Run `python3 scripts/validate_security_contract.py` on every security gate. It
checks schema, unique cases, required path/principal coverage, credential
substitution, anonymous/private denial, environment separation, trusted headers,
and risk-register completeness. Ticket 06 adds live end-to-end execution; until
then this validator is the executable policy oracle.

## 7. Risk register

Open Critical or High risks block their owning ticket and all descendants; they
do not make this contract incomplete because this ticket's purpose is to identify,
assign and gate them. Closing a risk requires a reproducer test and evidence in
the owning ticket. `Security Owner` adjudicates severity; the named implementation
owner fixes it.

| ID | Severity | Status | Reproduction / evidence | Intended control | Owner | Ticket |
|---|---|---|---|---|---|---|
| R-001 | Critical | Closed | `backend/internal/config/config_test.go` proves Release loads its USER credential only from the `backend_middleware_token` secret; `backend/internal/auth/auth_test.go` proves Backend does not validate OIDC and rejects JWT-shaped tokens; `middleware/internal/gateway/proxy_test.go` proves Middleware creates the Backend credential only from a valid session. | Backend accepts only the Middleware credential or an Account token; Middleware is the single OIDC validator and keeps separate service identities. | Backend Owner | 02, 03, 05 |
| R-002 | Critical | Closed | `middleware/internal/gateway/gateway_test.go` proves confidential Authorization Code, state/nonce/S256 PKCE, one-time callback, secure server session, expiry/logout, fixation resistance, and every listed failure; `frontend/test/oidc-browser.test.js` proves the browser flow has no token storage. | Confidential Middleware plus browser/session negative tests; live edge/IdP repetition is gated by Tickets 09–10. | Identity Owner | 04 |
| R-003 | Critical | Closed | `middleware/internal/gateway/proxy_test.go` proves session-before-credential, denial without upstream contact, trusted-header removal, route-specific credential substitution, auth-failure translation, and response non-disclosure. | Middleware verifies its own session, strips boundary headers, and substitutes credentials only per the authorization matrix. | Middleware Owner | 05 |
| R-004 | High | Open | `backend/main.go` defaults to `:<PORT>` and a host run exposes Backend on all interfaces. | Container-only private bind/network; no host port publication; automated port checks. | Platform Owner | 08 |
| R-005 | Critical | Closed | `scripts/test/test_production_secrets.py` rejects weak, missing, duplicate, wrong-namespace and disclosed values; `scripts/production-secrets.ps1` applies and validates Windows ACLs without printing values. | Destination-generated secret mounts, strength/isolation validation, ACLs and disclosure tests. | Security Owner | 02 |
| R-006 | High | Closed | `backend/internal/database/account_encryption_test.go` proves plaintext migration, AES-GCM storage, keyed lookup, transaction propagation, and wrong-key failure; Ticket 13 retains encrypted-backup controls. | Encrypt Account tokens at rest with a destination key, preserve the key with protected backups, and fail closed on a mismatched retained database. | Backend Owner | 02, 13 |
| R-007 | High | Open | There is no release Nginx configuration proving input-header removal, rate/request limits, or the full header policy. | Implement and externally test the section 5 edge policy and security headers. | Edge Owner | 09 |
| R-008 | High | Closed | `production_secrets.py` generates environment-prefixed namespaces, requires exact file/mount allowlists, and compares Test/Release without disclosing values; production service loaders reject direct or opposite-environment sources. | Environment-specific secret/config namespaces and lint rejecting opposite-environment identifiers. | Release Owner | 02, 11 |
| R-009 | High | Open | Current Windows distribution starts Backend, Agent and LLM on host loopback ports rather than an isolated Compose network. | Production containers expose no private host ports and accept only named-network callers. | Platform Owner | 08 |
| R-010 | Medium | Open | Provider base URLs and external responses cross an outbound trust boundary; current release gate has no archived SSRF/timeout/size assessment. | Scheme/target policy, TLS, bounded timeout/size, dependency audit and secret-safe diagnostics. | LLM Owner | 06 |
| R-011 | High | Open | No Test/Release OIDC registrations, keys, sessions, subjects or hostnames are presently proven isolated. | Provision isolated realms/tenants and lint/certify concrete section 4 values. | Identity Owner | 09, 11 |
| R-012 | Medium | Open | Current artifacts do not prove environment-specific backup, restore, retention or SQLite-consistent shutdown. | Explicit volumes plus tested backup/restore/retention and ordered shutdown. | Operations Owner | 08, 13 |
| R-013 | High | Open | Current build/release flow can build binaries locally and does not prove immutable Test-to-Release image promotion. | Traceable image build, SBOM/scans, digest-only promotion, and Release no-build preflight. | Release Owner | 07, 10, 14 |
| R-014 | Medium | Open | Current release artifacts do not declare or test an outbound dependency allowlist for registry, IdP, DNS/time, market, and model endpoints. | Document concrete endpoints; restrict egress where supported; test timeout/fail-closed behavior and secret-safe errors. | Platform Owner | 09, 14 |

## 8. Change and acceptance rules

The Security Owner, Identity Owner, Backend Owner, Middleware Owner, Edge Owner,
Platform Owner, LLM Owner, Data Owner, Operations Owner and Release Owner are
named roles; the deployment record must map each role to one accountable person
before Ticket 06 sign-off. Contract changes require review by the affected owner
and a passing validator. No exception may weaken Test/Release isolation. A lower
severity exception needs owner, compensating control and expiry; Critical/High
exceptions block promotion.
