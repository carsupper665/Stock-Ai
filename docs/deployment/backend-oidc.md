# Backend OIDC identity boundary

The Backend accepts an environment-local OIDC access token as the configured
USER identity. It validates an exact issuer, audience membership, exact stable
subject, `exp`, optional `nbf`, JOSE `typ`, RS256 algorithm, `kid`, and RSA
signature. It does not consume identity headers.

Configure these non-secret values together:

| Setting | Meaning |
|---|---|
| `OIDC_ISSUER` | Exact HTTPS `iss` claim |
| `OIDC_AUDIENCE` | Required string in `aud` |
| `OIDC_USER_SUBJECT` | Exact stable USER `sub`; never a display name |
| `OIDC_JWKS_URL` | HTTPS public signing-key endpoint |
| `OIDC_TOKEN_TYPE` | Required JOSE `typ`; defaults to `at+jwt` |

JWKS requests have a three-second request/context timeout, a 1 MiB response
read bound, and a maximum of 32 keys. Supported keys are cached for 15 minutes.
An unknown `kid`, or a signature failure for a cached `kid`, causes one safe
refresh so normal key rotation works. Refreshes use a 30-second cooldown and a
single in-flight generation: concurrent misses wait for and reuse that result,
while cached valid keys remain readable. Repeated unknown keys, invalid
signatures, and failed JWKS calls cannot trigger another request until the
cooldown expires. An unavailable or invalid JWKS fails authentication closed.
Errors returned to clients contain no token, claims, keys, or endpoint details.

`GET /v1/me` requires authentication and returns `kind`, `user_name`, and only
the identifier appropriate to that identity: `subject` for OIDC USER or
`account_id` for Account. It never returns a credential.

Opaque Account tokens retain their existing Backend-only lookup and permission
rules. A three-segment JWT-shaped value is always treated as OIDC and never
falls back to static USER or Account lookup after failure.

Static USER authentication is disabled by default. Local development (or a
non-Release test fixture) must deliberately set both
`ALLOW_STATIC_USER_TOKEN=true` and `USER_TOKEN`. Release startup rejects the
setting. Test and Release should use OIDC.
