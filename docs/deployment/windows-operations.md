# Windows operator workflows

`scripts/windows-operations.ps1` is the non-interactive entry point for the
Linux-container Compose deployment. It operates only on
`deploy/compose/compose.yaml` and its explicit environment file. The legacy
mixed Windows/Linux distribution is excluded: this workflow never launches
the distribution `.exe` files and rejects Compose files with a different
service set, host-published application ports, or local image builds. Release
preflight, Start, and Status require every application image to use an
immutable digest. Isolated Test deployments may use temporary tags while they
are being exercised; Update always requires promoted immutable digests from
Ticket 10.

## Install and preflight

Install Docker Desktop with the Linux container backend, place the approved
Compose env file and secret files at their configured paths, and run the
workflow from any directory. Quote every path. The paths below intentionally
contain spaces:

```powershell
$Ops = 'C:\Program Files\Stock AI\scripts\windows-operations.ps1'
$EnvFile = 'C:\ProgramData\Stock AI\Test\compose.env'
$Evidence = 'C:\ProgramData\Stock AI\Test\operator evidence'
& $Ops -Action Install -EnvFile $EnvFile -EvidenceDirectory $Evidence
& $Ops -Action Preflight -EnvFile $EnvFile -EvidenceDirectory $Evidence
```

`Install` validates prerequisites and inputs; it does not install or alter
Windows features. Preflight checks supported Windows releases, 8 GiB RAM,
20 GiB free system-drive space, CPU firmware virtualization, Docker engine
availability and Linux mode, required input files, Compose interpolation,
stack isolation, and the standard public ports 80/443 plus
optional comma-separated `STOCK_AI_PUBLIC_PORTS`. It writes a non-secret
`preflight.json` report and fails nonzero on any failed check.

## Start, inspect, stop, diagnose

```powershell
& $Ops -Action Start -EnvFile $EnvFile -EvidenceDirectory $Evidence
& $Ops -Action Status -EnvFile $EnvFile -EvidenceDirectory $Evidence
& $Ops -Action Diagnose -EnvFile $EnvFile -EvidenceDirectory $Evidence
& $Ops -Action Stop -EnvFile $EnvFile -EvidenceDirectory $Evidence
```

Start waits for each service's Compose health check and gives a failing service
and the diagnostics file location on timeout. Stop sends graceful Compose
stops in dependency order: Console, Agent Server, LLM Provider Server, then
Backend. It verifies that no application container remains running. It does
not remove volumes. Status writes `status.json` with short container ID,
the digest parsed from an immutable configured image reference (or `null` for
a temporary Test tag), image reference, health, restart count, writable
container disk bytes, volume names, Docker disk usage, certificate
expiry for certificates under `STOCK_AI_SECRETS_DIR`, HTTPS IdP issuer
reachability, and the latest file in optional `STOCK_AI_BACKUP_DIR`. To include
certificates stored elsewhere, set non-secret `STOCK_AI_CERTIFICATE_FILES` to
a semicolon-separated list of certificate paths. It does
not serialize environment values, container environment, logs, secret values,
or certificate contents. For a Test tag, `digest` is `null` and
`image_reference` reports the configured tag. Diagnose writes only service
names, short container IDs, health/state, exit codes, restart counts, and
whether Docker reported an error.

The Compose stack uses `restart: unless-stopped`; after Windows or Docker
Desktop restarts, run Status to inspect recovery and Start to bring up any
stopped services and wait for health. Do not use `docker compose down --volumes`
or remove named volumes.

## Update and deterministic rollback

Ticket 10's promotion handoff must provide a JSON manifest with `status` set
to `promoted`, and an `images` object containing exactly the four service
names mapped to full `repository@sha256:<64 lowercase hex>` references. A
sidecar `SHA256SUMS` must contain the manifest's SHA256 line. Example shape:

```json
{
  "status": "promoted",
  "images": {
    "backend": "registry.example/backend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "llm-provider-server": "registry.example/llm@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "agent-server": "registry.example/agent@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "console": "registry.example/console@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
  }
}
```

Update verifies the manifest checksum and digest syntax before changing the
environment file. It records timestamped rollback metadata with the previous
and promoted image references, Compose hash, prior env-file hash, and
promotion-manifest hash. No env-file copy or secret value is written to
evidence. If update or health checks fail, it restores the prior image
references and attempts to start the prior stack.

```powershell
$Promotion = 'C:\ProgramData\Stock AI\Test\promotion\promotion.json'
& $Ops -Action Update -EnvFile $EnvFile -EvidenceDirectory $Evidence `
  -PromotionManifest $Promotion
$Rollback = Get-ChildItem -LiteralPath $Evidence -Filter 'rollback-*.json' |
  Sort-Object Name -Descending | Select-Object -First 1
& $Ops -Action Rollback -EnvFile $EnvFile -EvidenceDirectory $Evidence `
  -RollbackMetadata $Rollback.FullName
```

Rollback reapplies only the recorded previous digests. It never rebuilds or
selects a floating tag. Preserve rollback metadata with the corresponding
deployment evidence.

For database/history recovery, use the separate encrypted full-set workflow in
[`backup-restore.md`](backup-restore.md). Image rollback does not restore
volumes or reverse database migrations.

## Windows and Docker restart acceptance

Run the operator commands from a checkout whose full path contains spaces;
the script resolves file paths literally and passes Docker arguments as an
array. For Docker Desktop restart evidence, use the existing two-phase
acceptance harness from a Windows PowerShell 7 session. Use the same unique
evidence directory for both phases, restart Docker Desktop between them, and
verify the generated summaries and persistent-state fingerprints:

```powershell
$TestScript = 'C:\Program Files\Stock AI\scripts\test-compose-stack.ps1'
$EnvFile = 'C:\ProgramData\Stock AI\Test\compose.env'
$Evidence = 'C:\ProgramData\Stock AI\Test\operator evidence\ticket-12'
& $TestScript -Phase BeforeDockerRestart -EnvFile $EnvFile -EvidenceDirectory $Evidence
# Restart Docker Desktop; wait for its Linux engine, then run:
& $TestScript -Phase AfterDockerRestart -EnvFile $EnvFile -EvidenceDirectory $Evidence
```

Install, Preflight, Start, Status, Diagnose, and Stop were exercised with the
spaced-path layout on Windows PowerShell 7.6.6. The formal Ticket 12 Exercise
and the `BeforeDockerRestart`/`AfterDockerRestart` phases also passed around a
real Docker Desktop restart; the sanitized reports and persistent-state
fingerprints are recorded in
[`ticket-12-windows-evidence.md`](ticket-12-windows-evidence.md) and
[`ticket-12-evidence/formal-compose-acceptance.json`](ticket-12-evidence/formal-compose-acceptance.json).
The official Exercise, `BeforeDockerRestart`, and `AfterDockerRestart` runs
passed. Release mode rejects mutable image references; Test mode permits its
temporary tags. Successful Update and metadata-driven Rollback remain pending
because no valid Ticket 10 promotion manifest is available. The invalid
checksum fixture proves rejection only; run Update and Rollback with the real
promoted manifest when Ticket 10 provides one.
