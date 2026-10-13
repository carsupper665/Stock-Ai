# Isolated Compose application stack

The single application definition is
`deploy/compose/compose.yaml`. It runs Backend, Agent Server, LLM Provider
Server, and Console. It does not build images and it never publishes an
application port on the host. Supply the approved Ticket 07 image references
through an environment-local env file; Ticket 10 replaces those references
with promoted digests.

## Boundaries and addressing

| Service | Networks | Compose DNS dependencies | Persistent volume |
|---|---|---|---|
| Backend | `application` | none | `${STOCK_AI_INSTANCE}_backend_state` (`backend.db`, application logs) |
| LLM Provider Server | `application` | `backend` (ordering only) | `${STOCK_AI_INSTANCE}_llm_state` (`llm-provider.db`) |
| Agent Server | `application` | `backend`, `llm-provider-server` | `${STOCK_AI_INSTANCE}_agent_state` (`agent-server.db`, Agent History) |
| Console | `application`, `edge` | all three private service names | none |

Only Console joins `${STOCK_AI_INSTANCE}_edge`; Ticket 09 attaches Nginx and
the OIDC Gateway to that designated edge. All four services use `expose` only,
which documents their container listeners without creating a host binding.
Backend, Agent Server, and LLM Provider Server cannot be reached by an edge-only
container. Docker network membership is not authentication; every existing
credential check remains mandatory.

The `application` bridge deliberately permits outbound traffic. Backend needs
HTTPS issuer/JWKS and market access, while LLM Provider Server needs its model
providers. Making this network `internal` would silently break those approved
outbound dependencies. It remains private because no listener is published and
only explicitly attached containers are members.

## Configuration and state

Copy `deploy/compose/compose.env.example` to a protected, environment-local
path outside Git. Set a unique `STOCK_AI_INSTANCE` and
`STOCK_AI_SECRET_NAMESPACE` for Test or Release. Point the image variables at
approved references and the secrets directory at the output of
`scripts/production-secrets.ps1`. Compose mounts only each service's allowlisted
secret files and the application reads them through `_FILE` variables.

Do not run `docker compose down --volumes` or `docker volume rm` during a stop,
update, or diagnostic operation. The explicit volume names are stable across
Compose `down`/`up`, host reboots, project-directory moves, and Docker Desktop
restarts. Test and Release must use different instance names and secret
namespaces; reusing either is a release blocker.

Validate interpolation before touching containers:

```powershell
docker compose --env-file C:\ProgramData\StockAI\Test\compose.env `
  -f .\deploy\compose\compose.yaml config --quiet
```

## Health ordering and shutdown

Every dependency uses `condition: service_healthy`; no process or Compose
command uses a fixed startup sleep. Creation order is Backend, LLM Provider
Server, Agent Server, Console. Compose removes or stops dependents in reverse:
Console, Agent Server, LLM Provider Server (the application "Gateway"), then
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
pwsh -NoProfile -ExecutionPolicy Bypass `
  -File .\scripts\test-compose-stack.ps1 `
  -Phase Exercise `
  -EnvFile C:\ProgramData\StockAI\Test\compose.env `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08
```

This validates resolved Compose configuration, network membership, absent host
bindings, health recovery after forced Agent/LLM/Backend crashes, a Compose
restart, graceful stop/start, `down`/`up`, all three SQLite integrity checks,
logical table counts, Agent History hashes, and restored health. It never
removes volumes.

Docker Desktop restart is deliberately a two-phase check because the validating
process cannot survive stopping its own engine:

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-compose-stack.ps1 `
  -Phase BeforeDockerRestart -EnvFile C:\ProgramData\StockAI\Test\compose.env `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08

# Restart Docker Desktop and wait for its engine to return.

pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-compose-stack.ps1 `
  -Phase AfterDockerRestart -EnvFile C:\ProgramData\StockAI\Test\compose.env `
  -EvidenceDirectory C:\ProgramData\StockAI\Test\evidence\compose-08
```

Archive the JSON summaries, crash container identities, stopped-database
fingerprints, and Agent History manifests. A leftover
`docker-restart-pending.json`, a failed SQLite integrity check, changed logical
state, a host port binding, or any unhealthy container means Ticket 08 has not
passed.
