# Cookie-based OIDC Gateway

The OIDC Gateway is the environment-local confidential client. It completes
Authorization Code login and keeps the access token in a server-side session;
the browser receives only an opaque, HMAC-authenticated session identifier.
The process is intentionally private and must have no published host port.

## Endpoints and edge contract

| Endpoint | Exposure | Purpose |
|---|---|---|
| `GET /oauth2/start` | public through Nginx | Creates one-time state, nonce, and PKCE verifier records, binds state to a short-lived signed HttpOnly browser cookie, and redirects to the IdP. `return` accepts only a local absolute path. |
| `GET /oauth2/callback` | public through Nginx | Atomically consumes state before exchange, validates the ID token and nonce, rotates the session ID, and redirects to a parameter-free application URL. |
| `GET /oauth2/me` | public through Nginx | Returns display identity and session expiry only. It never returns an access or ID token. |
| `POST /oauth2/logout` | public through Nginx | Requires the exact application `Origin`, deletes the session, expires the cookie, and navigates to the environment's IdP logout URL. |
| `GET /oauth2/auth` | Nginx `internal` auth subrequest only | Returns verified identity headers and the access token to Nginx. A browser route to this endpoint is forbidden. |

Nginx must send cookies only to the Gateway/Console boundary. It must mark the
`/oauth2/auth` location `internal`, discard its body, and copy its response
headers only into the private Console request described by the trusted-header
contract. It must not proxy these headers back to the browser. Ticket 09 owns
the concrete edge configuration and its external tests.

The session cookie is the exact environment-specific `__Host-` name and is
always `Secure; HttpOnly; SameSite=Lax; Path=/` with no `Domain`. Server-side
sessions expire at the earliest of the access-token lifetime, ID-token expiry,
or eight hours. Sessions and pending login records are held only in Gateway
memory; restart invalidates them. Run one Gateway replica unless a later design
adds a protected, environment-local shared session store with atomic consume.

The short-lived login-transaction cookie deliberately uses the environment-specific
name `__Secure-stock-ai-test-login` or `__Secure-stock-ai-release-login` with
`Secure; HttpOnly; SameSite=Lax; Path=/oauth2/callback` and no `Domain`. A narrow
path is not legal for the `__Host-` prefix, so the transaction cookie MUST retain
the `__Secure-` prefix; regression tests assert this exact browser prefix/path
contract. It contains only an authenticated opaque state binding and is deleted
on every callback.

## Required configuration

The service reads non-secret settings from the environment and the two secrets
only from namespaced files. Direct secret environment values are rejected.

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
| `OIDC_TOKEN_TYPE` | access token `typ`, default `at+jwt` | same as Backend |
| `OIDC_AUDIENCE` | API audience in the access token | same as Backend |

Authorization, token, JWKS, and end-session URLs must use the issuer's HTTPS
origin; their paths follow the IdP's discovery document. Redirect and post-logout URLs must share one non-local HTTPS
host and contain no wildcard, userinfo, fragment, or query. `localhost`, any
`*.localhost` alias (with or without a trailing dot), the entire IPv4 loopback
range, and IPv6 loopback are rejected in both Test and Release. The cookie key is
canonical base64 for exactly 32 bytes. See `oidc_gateway/.env.example` for the
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

The Gateway log records why a login failed without tokens or secrets: the token
endpoint status and OAuth `error` code, an ID-token rejection, or the access-token
field that the Backend would reject (`typ`, `alg`/`kid`, `iss`, `aud`, `sub`) with
its actual and configured values. A login whose access token the Backend would
reject never gets a session, so it cannot turn into API 401s after login.

`oidc-gateway preflight` checks the deployed configuration against the IdP from
inside the Gateway image and prints the Redirect URI and Post-logout URI to
register; at startup the Gateway logs the same two URIs.

Gateway JWKS handling accepts at most 32 supported RSA signing keys. Each
accepted key must have a non-empty unique `kid`, optional `use=sig`, optional
`alg=RS256`, an RSA modulus of at least 2048 bits, and an odd exponent of at
least three. Weak keys, even exponents, empty key sets, oversized sets, and
duplicate supported `kid` values fail validation.

