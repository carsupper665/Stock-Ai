# Ticket 12 Windows operator evidence

Run: 2026-10-13 (UTC). The operator script and Compose file were invoked from
the WSL worktree path `.../.ticket 12 validation/...`, which contains spaces,
using Windows PowerShell 7.6.6 and the Windows Docker CLI. Host: Windows 10 Pro
build 19045, 63.1 GiB RAM, 218.5 GiB free on the system drive. Docker Engine
was Linux mode, version 29.5.2.

The isolated project was `kit-t12-ops-smoke` with a unique Test instance and
volume namespace, no host-published application ports, and the Ticket 08 Test
secret directory by reference. The four local Test images were `0.7.0` tags;
Docker reported no `RepoDigest` for them.

## Successful Windows runs

- Install and Preflight passed Windows/Docker mode, resource, input-file,
  Compose config, stack isolation, and port checks.
- Start brought all four services to healthy state within the health wait.
- Status and Diagnose wrote sanitized JSON. Status contained no environment
  values, secret values, container environment, or logs.
- Stop shut down Console, Agent Server, LLM Provider Server, then Backend and
  verified no application container remained running. No volumes were removed.
- The formal `Exercise` phase passed, including crash recovery for Agent Server,
  LLM Provider Server, and Backend; Compose restart; stop/start; and down/up.
  SQLite fingerprints and Agent History manifests matched at every checkpoint.
- `BeforeDockerRestart` and `AfterDockerRestart` both passed around a real
  `docker.exe desktop restart`. Docker returned in Linux mode at 29.5.2, all
  four T12 services passed health checks, and the SQLite/History fingerprints
  still matched. All four T8 services also returned healthy. The Compose
  workflow did not remove volumes.

Sanitized runtime reports are in this directory:

- [Preflight](ticket-12-evidence/preflight.json)
- [Status before restart](ticket-12-evidence/status-before-docker-restart.json)
- [Status after restart](ticket-12-evidence/status-after-docker-restart.json)
- [Diagnose](ticket-12-evidence/diagnostics.json)
- [Crash recovery flags](ticket-12-evidence/crash-recovery.json)
- [SQLite fingerprints before restart](ticket-12-evidence/sqlite-before.json)
- [SQLite fingerprints after restart](ticket-12-evidence/sqlite-after.json)
- [Formal Compose Exercise and Docker restart evidence](ticket-12-evidence/formal-compose-acceptance.json)

The first harness attempt exposed a Windows SQLite failure on the WSL UNC
evidence path. The harness now copies stopped databases into a Windows-local
temporary directory before inspection, then writes only sanitized fingerprints
to the evidence directory. The final formal Exercise and restart phases passed
with this fix; the evidence bundle records checkpoint equality without copying
database contents into the repository.

## Promotion-dependent checks still pending

The Test images available on this host have mutable tags and no repository
digests. Preflight and Start accept those Ticket 08 Test references. Update
requires a valid Ticket 10 promotion manifest and SHA256SUMS; none is available
yet. A deliberately invalid checksum fixture was rejected before the env file
or containers changed; that is only rejection-path evidence, not a successful
update. A successful Update and metadata-driven Rollback remain unverified
until the real Test promotion manifest exists. The Test fixture also has no
configured backup directory or certificate files, and its sample IdP issuer
was unreachable, so those Status values report unavailable/false.
