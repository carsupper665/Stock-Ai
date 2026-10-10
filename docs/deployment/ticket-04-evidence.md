# Ticket 04 acceptance evidence

**Control:** cookie-based OIDC Gateway login  
**Evidence scope:** deterministic repository tests; real Test IdP and edge
certification remain Tickets 09–10.

Run from the repository root:

```text
cd oidc_gateway && go test -race ./... && go vet ./...
cd frontend && npm test
cd frontend && npm run build
rg "sessionStorage|USER TOKEN|USER_TOKEN|VITE_DEV_TOKEN" frontend/dist
```

Recorded result on 2026-10-11:

- Gateway: 42 tests/subtests passed across two packages under the race detector.
- Frontend: 54 tests passed across 10 files.
- Production frontend: 82 modules transformed; build completed successfully.
- Production bundle forbidden-token-UI scan: no matches.
- `git diff --check`: clean.

The Gateway tests act as a browser at the HTTP boundary and prove:

| Checklist item | Automated evidence |
|---|---|
| First login | Authorization Code parameters include unpredictable state and nonce plus S256 PKCE; state is bound to a signed HttpOnly `__Secure-<environment>-login; Path=/oauth2/callback` transaction cookie accepted by browser prefix rules; token request uses HTTP Basic confidential-client authentication and the stored verifier. |
| Secure session/fixation | An attacker-selected cookie is ignored and replaced; the new cookie is opaque, host-only, `Secure`, `HttpOnly`, `SameSite=Lax`, and `Path=/`. `/me` contains no access token. |
| Refresh | Repeated `/oauth2/me` calls retain the server session without browser token storage. |
| Expiry | Server expiry returns 401 and clears the cookie; the frontend expiry timer clears displayed identity and navigates to login. |
| Logout | Cross-origin POST is rejected; exact-origin logout destroys the server session, clears the cookie, and uses the configured post-logout URI. |
| One-time callback | A successful callback cannot be replayed. Both success and error callbacks redirect away from callback parameters. |
| Listed failures | Invalid state, nonce mismatch, PKCE failure, token exchange failure, IdP denial, replay, invalid session MAC, and expired session all remain unauthenticated. |
| No JS-visible OIDC token | Frontend login, current-user, expiry, and logout tests use only navigation or same-origin cookie requests; storage remains empty. The production bundle contains no static-token UI/storage markers. |
| Configuration isolation | Test and Release negative cases reject `localhost`, trailing-dot and subdomain localhost aliases, IPv4 loopback, and IPv6 loopback URLs. |
| JWKS strength/ambiguity | Negative cases reject RSA moduli below 2048 bits, even exponents, and duplicate supported `kid`; a valid 2048-bit odd-exponent key remains accepted. |

The tests use an in-process TLS-equivalent IdP transport because the repository
sandbox prohibits opening loopback listeners. The actual HTTP requests,
form encoding, Basic authentication, JWT signature/claim validation, callback
responses, cookies, and redirects are exercised; only the network socket is
substituted. Ticket 09 must repeat this through the real Test Nginx and IdP.

