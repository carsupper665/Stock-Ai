# Isolated Compose application stack

The single application definition is
`deploy/compose/compose.yaml`. It runs Backend, Agent Server, LLM Provider
Server, and the middleware. It does not build images and it never publishes an
application port on the host. Supply the approved Ticket 07 image references
through an environment-local env file; Ticket 10 replaces those references
with promoted digests.

## Boundaries and addressing

| Service | Networks | Compose DNS dependencies | Persistent volume |
|---|---|---|---|
| Backend | `application` | none | `${STOCK_AI_INSTANCE}_backend_state` (`backend.db`, application logs) |
| LLM Provider Server | `application` | none | `${STOCK_AI_INSTANCE}_llm_state` (`llm-provider.db`) |
| Agent Server | `application` | `backend`, `llm-provider-server` | `${STOCK_AI_INSTANCE}_agent_state` (`agent-server.db`, Agent History) |
| Middleware | `application`, `edge` | all three private service names | none |

Only the middleware joins `${STOCK_AI_INSTANCE}_edge`; Ticket 09 attaches Nginx
to that designated edge. All four services use `expose` only,
which documents their container listeners without creating a host binding.
Backend, Agent Server, and LLM Provider Server cannot be reached by an edge-only
container. Docker network membership is not authentication; every existing
credential check remains mandatory.

The `application` bridge deliberately permits outbound traffic. Backend needs
market access, while LLM Provider Server needs its model
providers. Making this network `internal` would silently break those approved
outbound dependencies. It remains private because no listener is published and
only explicitly attached containers are members.

## Configuration and state

Create a fresh Compose env file in WSL `/tmp`, outside Git, with mode `0600`.
It contains non-secret settings only; keep the existing verified secret files
in the Windows secrets directory and reference that directory by path. Set a
unique `STOCK_AI_INSTANCE` and
`STOCK_AI_SECRET_NAMESPACE` for Test or Release. Point the image variables at
approved references and the secrets directory at the output of
`scripts/production-secrets.ps1`. Compose mounts only each service's allowlisted
secret files and the application reads them through `_FILE` variables. The
middleware also carries the full OIDC settings (required interpolation) and the
`oidc_client_secret` and `oidc_cookie_key` secrets.

For a Test acceptance run, start from the checked-in example, then replace its
example OIDC settings (issuer, endpoints, client ID, subject) and image references with the approved Test values.
Do not place this env file under `C:\ProgramData` or put secret contents in it.
Keep the secrets directory reference, for example
`STOCK_AI_SECRETS_DIR=C:/ProgramData/StockAI/Test/secrets`.

```bash
compose_env="$(mktemp /tmp/stock-ai-test-compose.XXXXXX.env)"
chmod 0600 "$compose_env"
cp deploy/compose/compose.env.example "$compose_env"
${EDITOR:-vi} "$compose_env"
compose_env_windows="$(wslpath -w "$compose_env")"
```

Use the resulting `compose_env_windows` UNC path as `-EnvFile` in the Windows
commands below. Keep it private and delete the temporary file after acceptance.

Do not run `docker compose down --volumes` or `docker volume rm` during a stop,
update, or diagnostic operation. The explicit volume names are stable across
Compose `down`/`up`, host reboots, project-directory moves, and Docker Desktop
restarts. Test and Release must use different instance names and secret
namespaces; reusing either is a release blocker.

Validate interpolation before touching containers:

```powershell
$ComposeEnvPath = Read-Host 'Paste the UNC path printed by wslpath -w'
docker compose --env-file $ComposeEnvPath `
  -f .\deploy\compose\compose.yaml config --quiet
```

## Health ordering and shutdown

Every dependency uses `condition: service_healthy`; no process or Compose
command uses a fixed startup sleep. Creation order is Backend, LLM Provider
Server, Agent Server, middleware. Compose removes or stops dependents in reverse:
middleware, Agent Server, LLM Provider Server (the application "Gateway"), then
Backend. This gives Agent work a chance to stop before its model gateway and
keeps Backend available until both are down.

Each service receives `SIGTERM` and a bounded grace period. Agent Server stops
its event loops and requests before closing SQLite. LLM Provider Server stops
admission, drains requests, and closes SQLite. Backend now catches SIGTERM,
drains HTTP for up to 15 seconds, stops matching/market work, and closes its
SQLite connection and log files. Docker sends `SIGKILL` only after the Compose
grace period expires.

## Windows persistence acceptance

Run the non-destructive acceptance exercise from a clean checkout with Windows
Docker Desktop in Linux-container mode. The evidence directory must be outside
Git and new for each run.

```powershell
$ComposeEnvPath = Read-Host 'Paste the UNC path printed by wslpath -w'
pwsh -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\test-compose-stack.ps1 `
  -Phase Exercise `
  -EnvFile $ComposeEnvPath `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08
```

This validates resolved Compose configuration, network membership, absent host
bindings, health recovery after forced Agent/LLM/Backend crashes, a Compose
restart, graceful stop/start, `down`/`up`, all three SQLite integrity checks,
logical table counts, Agent History hashes, and restored health. It never
removes volumes.

The Ticket 08 acceptance uses the Exercise phase above. Car waived the separate
Docker Desktop daemon restart check for this ticket; it does not gate
completion. For deployments that require that additional local check, the
harness supports two phases because the validating process cannot survive
stopping its own engine:

```powershell
$ComposeEnvPath = Read-Host 'Paste the UNC path printed by wslpath -w'
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-compose-stack.ps1 `
  -Phase BeforeDockerRestart -EnvFile $ComposeEnvPath `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08

# Restart Docker Desktop and wait for its engine to return.

pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-compose-stack.ps1 `
  -Phase AfterDockerRestart -EnvFile $ComposeEnvPath `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08
```

Archive the Exercise JSON summary, crash recovery flags, stopped-database
fingerprints, and Agent History manifests. A failed SQLite integrity check,
changed logical state, a host port binding, or any unhealthy container means
the Exercise did not pass.
