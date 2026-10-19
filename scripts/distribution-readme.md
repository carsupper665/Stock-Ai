# Stock AI — Windows x64 distribution

Requirements: Windows x64 and PowerShell 7. Go, Node, npm and Docker are not needed.
Start with `pwsh -File ./start.ps1` from this folder.

Architecture: nginx serves the frontend and sends `/oauth2`, `/v1`, `/agent` and `/llm`
to `middleware.exe`, the only login (OIDC) layer. The middleware adds internal tokens
and forwards to Backend, Agent and LLM Provider, which listen on loopback only.

## Before the first start

1. Prepare an OIDC IdP client with redirect URI `https://<host>/oauth2/callback` and
   post-logout URI `https://<host>/`.
2. Prepare a domain `<host>` pointing at this machine and a TLS certificate and key for it.
3. Edit `.env`: set `RELEASE_APP_HOST` (`host` or `host:port`; nginx listens on that
   port, 443 if omitted), `RELEASE_TLS_CERT`, `RELEASE_TLS_KEY` and every `OIDC_*` value (`OIDC_USER_SUBJECT` is the one IdP subject
   allowed to sign in). All OIDC URLs must be non-local HTTPS.
4. Put the IdP client secret (at least 32 bytes) in
   `secrets/stock-ai-release_oidc_client_secret`. The build never creates it.

## Files

- `nginx/`: official nginx for Windows (pinned version, SHA-256 verified at build).
  `nginx/html/` is the frontend; `nginx/conf/release-*` are the shipped templates that
  `start.ps1` turns into `stock-ai-nginx.conf` / `stock-ai-edge.conf` on each start.
- `middleware.exe`: OIDC login and authenticated reverse proxy (runs in Release mode).
- `backend.exe`, `llm-provider-server.exe`, `agent-server.exe`: the internal services.
- `.env`: non-secret settings and `*_FILE` paths only.
- `secrets/stock-ai-release_<name>`: one file per secret. Generated on the first build
  (only when `.env` does not exist) from `-ConfigurationFile` or new random values;
  `backend_user_token` is used only by Agent and differs from `backend_middleware_token`.
  Keep `llm_master_key` when moving a database with encrypted Provider tokens.
- `data/`, `logs/`: created on first start; preserved on rebuilds (nginx logs are in `logs/`).

## Building

Run `scripts/build-distribution.ps1`. It downloads nginx 1.30.6 from nginx.org and
rejects an archive whose SHA-256 differs from the pinned value; `-NginxArchive <zip>`
(with `-NginxSha256`) uses a local archive instead. Stop every process running from
`dist/` first; a locked artifact fails preflight without changing the release.
Everything is prepared before publication, with backup and rollback if replacement
fails. `.env`, `secrets/`, databases, History and logs are never modified by a rebuild.
The first build fails if a launch-configuration token the middleware uses is shorter
than 32 bytes.

`-SkipFrontendBuild` requires an existing non-empty `frontend/dist`. Optional CLI tools:
`-CodexExecutable <bin/codex.exe of the complete official package>` and
`-ClaudeExecutable <claude.exe>`; a missing one is an error. A new `.env` points the
matching `LLM_SERVER_*_EXECUTABLE` setting at `tools/`; an existing `.env` is yours to
update.

Databases start empty. To migrate, stop all services using the original DBs, copy the
three databases and the Agent History directory into `data/`, and keep `llm_master_key`.

## Stopping

Stop nginx with `nginx\nginx.exe -s stop -p nginx -c nginx\conf\stock-ai-nginx.conf`
from this folder. Stop the other services with Ctrl+C in their windows: Agent first,
then middleware, LLM Provider and Backend.
