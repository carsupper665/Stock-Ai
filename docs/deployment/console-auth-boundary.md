# Console authentication boundary

Console is the only application service reachable from the edge. It treats the
Gateway headers as assertions, not proof by themselves. For every session-bound
`/v1`, `/agent`, and `/llm` request it verifies the access-token RS256 signature,
JOSE token type, exact issuer, audience membership, exact configured subject,
expiry, and optional not-before claim against the environment-local JWKS. The
verified token subject must also exactly match the single
`X-Auth-Request-Subject` value supplied by the Gateway path. Missing, duplicated,
spoofed, invalid, or mismatched assertions fail closed before upstream contact.

Console uses the same non-secret OIDC settings as Backend:

| Setting | Requirement |
|---|---|
| `OIDC_ISSUER` | Exact environment-local HTTPS issuer |
| `OIDC_AUDIENCE` | Required API audience |
| `OIDC_USER_SUBJECT` | Exact stable allowed USER subject |
| `OIDC_JWKS_URL` | HTTPS public signing-key endpoint |
| `OIDC_TOKEN_TYPE` | Required JOSE `typ`; defaults to `at+jwt` |

Console no longer loads or accepts `USER_TOKEN`. Its private service credentials
remain namespace-scoped secret files in Test and Release.

## Request semantics

The two authorization paths are deliberately non-conflicting:

- `/v1` with no browser `Authorization` requires a verified Gateway assertion.
  Console strips every boundary header and sends `Authorization: Bearer
  <verified-access-token>` to Backend. Backend verifies the token independently.
- `/v1` with an explicit, syntactically valid, opaque Bearer is Account
  delegation. Console strips Gateway/forwarding/cookie headers and passes that
  Bearer only to Backend, even if the browser also has a Gateway session. Backend
  remains the authority for Account-token validity and permissions. JWT-shaped
  browser Bearers are rejected so this path cannot become a second USER login.
- `/agent` and `/llm` never accept browser Authorization. An explicit Account
  bearer receives 403; a missing or invalid USER assertion receives 401. Neither
  denial contacts an upstream.
- After USER verification, a method-aware route table permits only the Agent
  health and Session resources (including runs, history/retry, events, memories,
  monitoring, and lifecycle operations), plus the LLM health, providers/tokens,
  harnesses, model-catalog, models, and generate resources. Unknown path shapes
  return 404 locally and known resources with a wrong method return 405 locally;
  neither receives a credential or contacts an upstream. Allowed Agent routes
  receive only Agent Admin. LLM provider/model-management routes receive only
  LLM Admin; `GET /llm/v1/models` and `POST /llm/v1/generate` receive only LLM
  Runtime.

Before every proxy request Console removes `Authorization`, `Cookie`,
`Forwarded`, all `X-Forwarded-*`, `X-Real-IP`, `X-Original-URI`,
`Proxy-Authorization`, remote-user aliases, and all `X-Auth-Request-*` headers,
then creates exactly the selected upstream Authorization value. The same
authentication-bearing headers are removed from upstream responses. Private
401/403 responses become a generic 502 so a rejected internal credential is not
misreported as an expired browser session.

Console removes the browser's `Accept-Encoding`, so Go's upstream transport may
negotiate and transparently decode gzip itself. It also explicitly decodes a
gzip response when transport auto-decompression is unavailable (for example a
range request), and rejects every unsupported or stacked content encoding.
Agent/LLM responses are then buffered as identity bytes up to 16 MiB and rejected
with a generic 502 if they exceed that bound or contain any configured internal
credential in a header, redirect, decompressed body, or HTTP trailer. All response
trailers and boundary headers are discarded after inspection. Console has no
request/header logger, and its generated errors contain no credential, token,
claim dump, JWKS address, or upstream body.
