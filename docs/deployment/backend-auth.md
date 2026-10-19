# Backend internal token boundary

The Backend does not verify OIDC, parse JWTs, or contact an IdP. Browser login
is handled only by the middleware (see `middleware.md`). The Backend accepts
three kinds of Bearer credential and never consumes identity headers.

| Credential | Setting | Meaning |
|---|---|---|
| middleware token | `USER_TOKEN` / `USER_TOKEN_FILE` (secret `backend_middleware_token`) | The USER identity. Held only by the Backend and the middleware; the middleware sends it on `/v1` after it has verified the browser session. |
| Agent service token | `AGENT_SERVICE_TOKEN` / `AGENT_SERVICE_TOKEN_FILE` (secret `backend_user_token`) | Held by the Agent. Must differ from `USER_TOKEN`; startup fails if they are equal. |
| Account token | none (stored per Account) | Opaque Account token with the existing Backend-only lookup and permission rules. |

`GET /v1/me` requires authentication and returns `kind`, `user_name`, and, for
an Account, `account_id`. It never returns a credential and no longer returns
`subject`.

The Agent service token only authorizes `GET /v1/accounts/:id`, including the
bound Account's runtime token. It does not authenticate `/v1/me`, list or modify
Accounts, or place orders. The Agent continues using each Account's own token for
domain tools. `AGENT_SERVER_BACKEND_USER_TOKEN_FILE` remains the Agent's setting
name for the same file. Deploy Backend and Compose changes together: an older
Backend does not consume `AGENT_SERVICE_TOKEN_FILE` and will continue rejecting
the Agent lookup.

`ALLOW_STATIC_USER_TOKEN` no longer exists. The middleware token is the only
USER credential in every environment; Test and Release read it from the
namespaced secret file, and local development may set `USER_TOKEN` directly.
Browsers never receive it, and the middleware passes a browser-supplied opaque
Account token through unchanged but rejects a JWT-shaped Bearer.
