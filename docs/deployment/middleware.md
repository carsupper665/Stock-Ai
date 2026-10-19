# Middleware (OIDC login and API proxy)

The middleware is the environment-local confidential client and the only login
layer. It completes Authorization Code login, keeps the access token in a
server-side session, and proxies `/v1`, `/agent`, and `/llm` to the private
services. The browser receives only an opaque, HMAC-authenticated session
identifier. Nginx serves the frontend static files directly without checking
login; an unauthenticated frontend sends the browser to the login page itself.
The process is intentionally private and must have no published host port.

## Endpoints and edge contract

| Endpoint | Exposure | Purpose |
|---|---|---|
| `GET /oauth2/start` | public through Nginx | Creates one-time state, nonce, and PKCE verifier records, binds state to a short-lived signed HttpOnly browser cookie, and redirects to the IdP. `return` accepts only a local absolute path. |
| `GET /oauth2/callback` | public through Nginx | Atomically consumes state before exchange, validates the ID token and nonce, rotates the session ID, and redirects to a parameter-free application URL. |
| `GET /oauth2/me` | public through Nginx | Returns display identity and session expiry only. It never returns an access or ID token. |
| `POST /oauth2/logout` | public through Nginx | Requires the exact application `Origin`, deletes the session, expires the cookie, and navigates to the environment's IdP logout URL. |

Nginx sends cookies only to the middleware and holds no token: it has no
`auth_request` and sees no access token or internal credential. The concrete
edge configuration is in `deploy/nginx/*-edge.conf.template`.

The session cookie is the exact environment-specific `__Host-` name and is
always `Secure; HttpOnly; SameSite=Lax; Path=/` with no `Domain`. Server-side
sessions expire at the earliest of the access-token lifetime, ID-token expiry,
or eight hours. Sessions and pending login records are held only in middleware
memory; restart invalidates them. Run one middleware replica unless a later design
adds a protected, environment-local shared session store with atomic consume.

The short-lived login-transaction cookie deliberately uses the environment-specific
name `__Secure-stock-ai-test-login` or `__Secure-stock-ai-release-login` with
`Secure; HttpOnly; SameSite=Lax; Path=/oauth2/callback` and no `Domain`. A narrow
path is not legal for the `__Host-` prefix, so the transaction cookie MUST retain
the `__Secure-` prefix; regression tests assert this exact browser prefix/path
contract. It contains only an authenticated opaque state binding and is deleted
on every callback.

## Required configuration

The service reads non-secret settings from the environment and every secret
only from namespaced files. Direct secret environment values are rejected. The
secrets are the OIDC client secret and cookie key below, plus the upstream
credentials `agent_admin_token`, `llm_admin_token`, `llm_runtime_token`, and
`backend_middleware_token` (`AGENT_SERVER_ADMIN_TOKEN_FILE`,
`LLM_SERVER_ADMIN_TOKEN_FILE`, `LLM_SERVER_RUNTIME_TOKEN_FILE`,
`BACKEND_USER_TOKEN_FILE`).

| Setting | Test | Release |
|---|---|---|
| `STOCK_AI_ENVIRONMENT` | `Test` | `Release` |
| `OIDC_CLIENT_ID` | IdP client ID (no whitespace) | IdP client ID (no whitespace) |
| `OIDC_COOKIE_NAME` | `__Host-stock-ai-test-session` | `__Host-stock-ai-release-session` |
| `OIDC_CLIENT_SECRET_FILE` | `.../stock-ai-test_oidc_client_secret` | `.../stock-ai-release_oidc_client_secret` |
| `OIDC_COOKIE_KEY_FILE` | `.../stock-ai-test_oidc_cookie_key` | `.../stock-ai-release_oidc_cookie_key` |
| issuer | exact HTTPS issuer of the Test IdP | exact HTTPS issuer of the Release IdP |
| redirect | `https://<test-app>/oauth2/callback` | `https://<release-app>/oauth2/callback` |
| post logout | `https://<test-app>/` | `https://<release-app>/` |
| `OIDC_TOKEN_AUTH_METHOD` | `client_secret_basic` (default) or `client_secret_post` | same |
| `OIDC_USER_SUBJECT` | exact stable allowed USER subject | same |
| `OIDC_TOKEN_TYPE` | access token `typ`, default `at+jwt` | same |
| `OIDC_AUDIENCE` | API audience in the access token | same |
| `BACKEND_URL`, `AGENT_SERVER_URL`, `LLM_SERVER_URL` | private upstream base URLs | same |

Authorization, token, JWKS, and end-session URLs must use the issuer's HTTPS
origin; their paths follow the IdP's discovery document. Redirect and post-logout URLs must share one non-local HTTPS
host and contain no wildcard, userinfo, fragment, or query. `localhost`, any
`*.localhost` alias (with or without a trailing dot), the entire IPv4 loopback
range, and IPv6 loopback are rejected in both Test and Release. The cookie key is
canonical base64 for exactly 32 bytes. See `middleware/.env.example` for the
complete non-secret shape; never commit real hostnames, subjects, or secrets.

The frontend has no token login form. It starts login by navigation, restores
the current user through `/oauth2/me`, sends same-origin cookies on API calls,
handles both timer and API-driven expiry, and logs out with a same-origin POST
navigation. A production build rejects a non-`oidc` auth-provider setting.

## Failure behavior

State must match both the server record and the initiating browser's signed
transaction cookie. It is deleted before denial handling or token exchange, so
callback replay cannot retry a code. Invalid/missing state, denial, PKCE/token exchange failure,
invalid ID-token signature or claims, nonce mismatch, and expired tokens all
expire any presented application cookie and redirect to a clean login URL with
only a generic error code. IdP descriptions, codes, tokens, claims, secrets,
and verifier material are never reflected. A successful callback always issues
a fresh random session ID, ignoring any attacker-planted cookie.

The middleware log records why a login failed without tokens or secrets: the token
endpoint status and OAuth `error` code, an ID-token rejection, or the access-token
field that the middleware would reject (`typ`, `alg`/`kid`, `iss`, `aud`, `sub`) with
its actual and configured values. A login whose access token fails these checks
never gets a session, so it cannot turn into API 401s after login.

`middleware preflight` checks the deployed configuration against the IdP from
inside the middleware image and prints the Redirect URI and Post-logout URI to
register; at startup the middleware logs the same two URIs.

Middleware JWKS handling accepts at most 32 supported RSA signing keys. Each
accepted key must have a non-empty unique `kid`, optional `use=sig`, optional
`alg=RS256`, an RSA modulus of at least 2048 bits, and an odd exponent of at
least three. Weak keys, even exponents, empty key sets, oversized sets, and
duplicate supported `kid` values fail validation.


## API proxy

For `/v1`, `/agent`, and `/llm` the middleware treats the session cookie as the
only browser identity. A missing or invalid session fails closed with 401 before
upstream contact. The browser's identity, forwarding, and cookie headers are never
forwarded, and the middleware sets exactly one upstream `Authorization` value:

- `/v1` with no browser `Authorization` requires a valid session. The middleware
  sends `Authorization: Bearer <backend_middleware_token>` to Backend.
- `/v1` with an explicit, syntactically valid, opaque Bearer is Account
  delegation. The middleware passes that Bearer unchanged to Backend without
  needing a session; Backend remains the authority for Account-token validity and
  permissions. JWT-shaped browser Bearers are rejected with 401 so this path
  cannot become a second USER login.
- `/agent` and `/llm` never accept browser Authorization. An explicit Account
  bearer receives 403; a missing or invalid session receives 401. Neither denial
  contacts an upstream.
- After session verification, a method-aware route table permits only the Agent
  health and Session resources (including runs, history/retry, events, memories,
  monitoring, and lifecycle operations), plus the LLM health, providers/tokens,
  harnesses, model-catalog, and models resources. Unknown path shapes return 404
  locally and known resources with a wrong method return 405 locally; neither
  receives a credential or contacts an upstream. Allowed Agent routes receive only
  the Agent admin token. `GET /llm/v1/models` receives only the LLM runtime token;
  every other allowed `/llm` route receives only the LLM admin token.
  `POST /llm/v1/generate` is not open to browsers.

Before every proxy request the middleware removes `Authorization`, `Cookie`,
`Forwarded`, all `X-Forwarded-*`, `X-Real-IP`, `X-Original-URI`,
`Proxy-Authorization`, remote-user aliases, and all `X-Auth-Request-*` headers,
then creates exactly the selected upstream Authorization value. The same
authentication-bearing headers are removed from upstream responses. Agent and LLM
401/403 responses become a generic 502 so a rejected internal credential
is not misreported as an expired browser session.

The middleware removes the browser's `Accept-Encoding`, so Go's upstream transport
may negotiate and transparently decode gzip itself. It also explicitly decodes a
gzip response when transport auto-decompression is unavailable (for example a
range request), and rejects every unsupported or stacked content encoding.
Agent/LLM responses are then buffered as identity bytes up to 16 MiB and rejected
with a generic 502 if they exceed that bound or contain any configured internal
credential in a header, redirect, decompressed body, or HTTP trailer. All response
trailers and boundary headers are discarded after inspection. The middleware has no
request/header logger, and its generated errors contain no credential, token,
claim dump, JWKS address, or upstream body.
