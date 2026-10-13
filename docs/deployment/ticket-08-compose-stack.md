# Ticket 08 Compose-stack evidence

Date: 2026-10-13 (Asia/Taipei)

## Reviewable implementation evidence

- `deploy/compose/compose.yaml` is the sole four-service application
  definition. The policy suite proves Backend, Agent Server, and LLM Provider
  Server have no host publication; only Console joins the designated edge;
  service URLs use Compose DNS; persistent state uses environment-named
  volumes; and every dependency is health-gated.
- The dependency graph creates Backend → LLM Provider Server → Agent Server →
  Console and therefore stops Console → Agent Server → LLM Provider Server →
  Backend. All services use SIGTERM, bounded grace periods, restart policy,
  read-only roots, dropped capabilities, and meaningful HTTP health probes.
- Backend was corrected to handle SIGTERM, drain HTTP, stop background work,
  and close SQLite. The process-level test sends the platform termination
  signal, requires exit code zero within the bound, and successfully reopens
  and migrates the same SQLite file afterward.
- `scripts/test-compose-stack.ps1` is the Windows acceptance harness. It
  exercises crash/recovery, Compose restart, stop/start, `down`/`up`, and the
  two sides of a real Docker Desktop restart without deleting a volume. Its
  stopped-state evidence runs `PRAGMA integrity_check`, records logical table
  counts/schema hashes, hashes Agent History, checks network membership and
  rejects host port bindings.

Checks completed in this worktree during this task:

```text
python3 -m unittest scripts.test.test_compose_stack scripts.test.test_compose_sqlite_evidence scripts.test.test_production_images scripts.test.test_production_secrets
GOCACHE=/tmp/wren-t8-go-cache go test ./test -run TestBackendProcessGracefullyClosesSQLiteOnPlatformSignal -count=1
python3 -m json.tool docs/deployment/ticket-08-evidence-run3.json
git diff --check master
```

Results: 23 Python policy/evidence/image/secret tests passed. The Backend
SIGTERM/SQLite process test passed after using an allowed temporary Go cache
and permission for its loopback listener. Compose CLI validation is currently
unavailable through WSL PATH; the static YAML policy tests passed. The actual
Windows Docker Exercise evidence below independently validates the resolved
runtime stack. Windows cross-compilation and `git diff --check` passed in the
preceding implementation session.

## Windows Docker Exercise evidence

Imported and checksum-verified from
`C:\ProgramData\StockAI\Ticket08Test\evidence-run3` using the absolute
Windows PowerShell 7 executable. Only the sanitized summary, recovery flags,
and file hashes are recorded in
`ticket-08-evidence-run3.json`; no database contents, compose env file, or
secrets were read or copied.

Exercise passed on Docker Linux Engine `29.5.2` at `2026-10-12T11:42:52Z`:
all four services were healthy, application host ports were absent, and
Agent, LLM Provider, and Backend each recovered after a forced crash. SQLite
integrity checks passed. SHA256 values for all three database files, the
logical SQLite fingerprints, and Agent History manifest matched across
Compose restart, stop/start, and `down`/`up` checkpoints.

Car waived the separate Docker Desktop daemon restart acceptance for Ticket
08. That check is excluded from the completion gate; no Before/After Docker
restart evidence is needed or cited here.
