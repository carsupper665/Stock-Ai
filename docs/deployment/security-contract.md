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
| Nginx edge | host TCP 443; TCP 80 redirect only | approved client networks | The only public application listener. Terminates TLS, applies limits and security headers, removes spoofable headers, and routes only the paths in the authorization matrix. |
| OIDC Gateway | container TCP 4180, no host publication | Nginx on the environment's edge network | Confidential OIDC client. Owns login state, nonce, PKCE verifier and application session cookie. Only its signed/validated result is an authenticated browser identity. |
| Console | container TCP 8088, no host publication | Nginx and OIDC Gateway on the edge network | Serves static files and is the only application upstream reachable by Nginx. It independently verifies the Gateway assertion/token before substituting private-service credentials. |
| Backend | container TCP 7794, no host publication | Console and Agent Server on the private application network | Validates an environment-local OIDC access token or Account token. It never trusts identity headers. `/v1/health` is private to container health checks. |
| Agent Server | container TCP 8080, no host publication | Console on the private application network | Requires its Admin credential for `/api/v1/*`; uses a dedicated Backend USER credential and LLM Runtime credential for outbound calls. |
| LLM Provider Server | container TCP 8090, no host publication | Console and Agent Server on the private application network | Admin credential controls configuration; Runtime credential permits model discovery/generation only. Provider secrets remain server-side. |
| IdP | stable environment-specific HTTPS TCP 443 | browsers, OIDC Gateway, and Backend/JWKS client | Separate Test realm/tenant and Release realm/tenant. The externally stable issuer string is used everywhere; no `localhost` issuer alias. |
| External model/market providers | outbound HTTPS TCP 443 only | LLM Provider Server or Backend, as explicitly configured | Untrusted responses. TLS validation, timeouts, response-size limits and secret-safe errors are required. No inbound trust is granted. |
| Container registry | outbound HTTPS TCP 443 during pull/promotion only | Test/Release Docker engine through the operator workflow | Digest-addressed read access. Release receives no source/build context and no push credential. |
| DNS, time and trust services | host-managed DNS/NTP and HTTPS certificate validation | Windows/Docker host and containers as required | Approved infrastructure only. Correct time and DNS are authentication dependencies; failure is fail-closed, never an issuer-hostname rewrite. |
| Persistent data | no network listener | only the service container named in section 3 | Environment-local volumes. No volume is mounted into Nginx or the OIDC Gateway unless explicitly listed below. |

The boundaries are: (1) browser/untrusted network → Nginx, (2) Nginx → OIDC
Gateway/Console edge network, (3) Console → private application network,
(4) application → environment-local persistent volumes, (5) application → IdP,
and (6) application → external providers. Network location is never an identity.
TLS is mandatory across boundary 1 and for boundaries 5–6. The isolated Docker
networks are the compensating transport control for boundaries 2–4; no private
listener may be published on a host interface.

## 2. Identities and permissions

| Identity | Authentication | Permissions | Explicitly forbidden |
|---|---|---|---|
| Anonymous browser | none | Public static assets, edge liveness, login start and one-time callback | `/v1` data, `/agent`, `/llm`, internal health endpoints, or any credential-bearing response |
| OIDC USER | Gateway session backed by a valid environment-local Authorization Code flow; Backend receives a valid access token | USER-level Backend APIs; Console may substitute Agent Admin, LLM Admin, or LLM Runtime credentials only for the matrix routes | Supplying or reading private-service credentials; using Test identity in Release or vice versa |
| Account token | Opaque Backend token in `Authorization: Bearer`; validated only by Backend | Account self-service, market reads, own trading and own messages as implemented by Backend | USER account administration, Agent/LLM routes, or conversion/fallback to OIDC USER |
| Console | Private workload plus independently verified OIDC USER assertion/token | Route to Backend; replace browser credentials with the one exact service credential required by an allowed `/agent` or `/llm` route | Trust browser identity headers, expose credentials, or call persistent stores directly |
| Agent Server | Agent Admin for inbound; dedicated Backend USER and LLM Runtime credentials outbound | Agent administration/runtime; Backend and model operations required for agent runs | LLM administration, browser session creation, or accepting Account tokens as Admin |
| LLM Provider Server | LLM Admin or LLM Runtime bearer, selected by endpoint | Admin: provider/token/catalog management. Runtime: model list and generation only | Returning Provider tokens/master key or accepting one credential in the other's role |
| Operator | Windows administrator plus documented deployment authorization | Manage configuration, Docker, certificates, backup/restore and promotion | Reusing Test secrets in Release, rebuilding on Release, or bypassing recorded gates |

Authentication is fail-closed. An invalid OIDC token MUST NOT fall back to the
legacy USER token or Account identity. Static USER-token login is development-only
and Release MUST reject it. Account-token requests have separate semantics:
Nginx/Gateway may pass an opaque Bearer request only to `/v1`; Console preserves
it unchanged; Backend alone resolves it. An Account bearer never unlocks `/agent`
or `/llm`.

## 3. Credentials, persistent assets, and dependencies

| Item | Owner/consumer | Storage and handling | Isolation/rotation |
|---|---|---|---|
| TLS private key/certificate | Edge Operator / Nginx | Windows-protected secret mount; private key never in image, Git, log or browser | Unique per environment/hostname; renew in place and test before activation |
| OIDC client secret | Identity Owner / OIDC Gateway | Environment secret namespace, server-side only | Independent confidential clients; rotate at IdP and Gateway together |
| OIDC cookie encryption/signing key | Security Owner / OIDC Gateway | Environment secret namespace | Independent random key and cookie name; rotation invalidates only that environment's sessions |
| IdP signing private key | Identity Owner / IdP only | IdP-protected key store; only public JWKS leaves the IdP | Independent per realm/tenant; Release private key never enters Test |
| Allowed USER subjects | Identity Owner / IdP, Backend | Environment configuration containing stable subject identifiers, not display names | Separate Test and Release allowlists; reviewed as authorization data |
| USER legacy token | Security Owner / Backend and Agent | Development/Test only when explicitly enabled | Forbidden in Release authentication; service replacement values remain environment-local |
| Account tokens | Backend / account holder | AES-GCM ciphertext plus a database-unique keyed lookup in the Backend database; plaintext exists only at issuance/authorized display/request validation; never logs or browser storage | Unique per account; duplicate create/rotation fails at the database; reset revokes prior token. The environment-specific encryption key is preserved with protected backups |
| Backend Account-token encryption key | Security Owner / Backend only | Environment secret namespace; mounted read-only and never stored with the Backend database | Unique per environment; preserve with retained database backups; rotation requires decrypt-and-re-encrypt |
| Agent Admin credential | Security Owner / Console, Agent | Secret mount, never frontend environment/build args | Unique per environment; rotate Console and Agent atomically |
| Agent→Backend USER credential | Security Owner / Agent, Backend | Secret mount | Distinct from browser/session credentials and unique per environment |
| LLM Admin credential | Security Owner / Console, LLM | Secret mount | Unique per environment; never usable as Runtime by policy |
| LLM Runtime credential | Security Owner / Console, Agent, LLM | Secret mount | Unique per environment; least privilege; rotate all consumers together |
| LLM master key | Security Owner / LLM | Secret mount separate from Provider database | Unique per environment; MUST be preserved with its encrypted DB; rotation requires an explicit re-encryption procedure |
| Provider API tokens | Application Owner / LLM | AES-GCM encrypted in LLM DB; decrypted only for provider call | Test uses non-production provider accounts/quotas; revoke on disclosure |
| Backend DB | Data Owner / Backend | Environment-specific volume; contains business data and Account tokens | Never cross-mounted; encrypted/ACL-protected backups under Ticket 13 |
| Agent DB and History | Data Owner / Agent | Environment-specific volumes | Never cross-mounted; backed up consistently with Backend state |
| LLM Provider DB | Application Owner / LLM | Environment-specific volume; paired with its master key | Never copied without its matching protected key and authorization |
| IdP database/key store | Identity Owner / IdP, when self-hosted | Dedicated environment-specific volume and protected backups | Never shared or cloned from Release into Test |
| Logs and backup sets | Operations Owner / producing services | Environment-specific bounded volumes/protected backup location | No secrets; retention and restore controls are Ticket 13 |
| Container images/SBOM/scan evidence | Release Owner | Registry artifacts addressed by immutable digest | Exact Test-approved digest promoted; Release never rebuilds |
| Registry pull credential | Release Owner / Docker engine | Destination-local credential store with pull-only scope | Different from Test/publisher credentials; rotate without changing image digest |
| IdP/JWKS | Identity Owner / Gateway, Backend | Outbound HTTPS using exact configured issuer | Cache is bounded and safe rotation is required; fail closed when unverifiable |
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

At the public listener Nginx MUST clear, before any routing/auth subrequest:

`Forwarded`, every `X-Forwarded-*`, `X-Real-IP`, `X-Original-URI`,
`X-Forwarded-Client-Cert`, `Proxy-Authorization`, `X-Remote-User`, `Remote-User`, `X-User`, `X-Email`,
`X-Auth-Request-User`, `X-Auth-Request-Email`, `X-Auth-Request-Preferred-Username`,
`X-Auth-Request-Subject`, and `X-Auth-Request-Access-Token`.

Nginx may create only normalized `X-Forwarded-For`, `X-Forwarded-Proto=https`,
`X-Forwarded-Host`, `X-Real-IP`, and a validated/generated `X-Request-ID` from
connection data. After a successful private Gateway auth subrequest it may copy
only these authenticated response headers to Console:
`X-Auth-Request-Subject`, `X-Auth-Request-User`, `X-Auth-Request-Email`, and
`X-Auth-Request-Access-Token`. The Gateway, not the browser, creates them.
Nginx never originates an authenticated identity value.

`Authorization` is handled by route rather than trusted as an identity header.
Nginx removes it on browser-session, authentication, static, `/agent`, and `/llm`
requests. It may preserve an opaque Bearer value only on `/v1`, where Console
passes it unchanged and Backend validates it as an Account token. For an OIDC
session Console discards browser Authorization and creates the Backend bearer
from the verified Gateway access token. `Cookie` is sent only to the Gateway and
Console session boundary and is removed before Backend, Agent, or LLM proxying.

Console MUST independently verify the access token or a signed assertion including
issuer, audience, subject and expiry before credential substitution. It strips all
authentication/forwarding headers before proxying to Backend, Agent or LLM, then
sets only the required Authorization credential. Backend never consumes identity
headers. Logs omit all authentication headers and cookies. Nginx configuration
tests and Ticket 05 spoofing tests must prove these rules.

## 6. Executable authorization matrix

`docs/deployment/authorization-matrix.csv` is normative under this section. Each
row is a request that later integration tests MUST execute at Nginx. `edge_action`
is one of `allow`, `redirect_login`, `deny`, or `not_found`; `upstream` is `none`,
`console`, `backend`, `agent`, `llm`, `oidc_gateway`, or `edge`. A denial with
`upstream=none` MUST be proven not to contact an application upstream.

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
| R-001 | Critical | Open | Start current Backend/Console with a static `USER_TOKEN`; the same bearer grants human USER and private-service elevation. | Production rejects static USER auth; validate OIDC token and separate service identities. | Backend Owner | 02, 03, 05 |
| R-002 | Critical | Open | No OIDC Gateway, authorization-code callback, state/nonce/PKCE, or secure session exists in the current tree. | Implement the confidential Gateway and browser/session negative tests. | Identity Owner | 04 |
| R-003 | Critical | Open | Current Console accepts the static USER bearer before injecting Agent/LLM credentials. | Independently verify environment-local OIDC identity; strip spoofed headers; replace credentials only per matrix. | Console Owner | 05 |
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

The Security Owner, Identity Owner, Backend Owner, Console Owner, Edge Owner,
Platform Owner, LLM Owner, Data Owner, Operations Owner and Release Owner are
named roles; the deployment record must map each role to one accountable person
before Ticket 06 sign-off. Contract changes require review by the affected owner
and a passing validator. No exception may weaken Test/Release isolation. A lower
severity exception needs owner, compensating control and expiry; Critical/High
exceptions block promotion.
