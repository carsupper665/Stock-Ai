# Ticket 08 Compose-stack evidence

Date: 2026-10-12 (Asia/Taipei)

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

Commands completed in this worktree:

```text
docker compose -f deploy/compose/compose.yaml config --quiet
python3 -m unittest scripts.test.test_compose_stack scripts.test.test_compose_sqlite_evidence scripts.test.test_production_images scripts.test.test_production_secrets
go test ./test -run TestBackendProcessGracefullyClosesSQLiteOnPlatformSignal -count=1
GOOS=windows GOARCH=amd64 go test -c -o <temporary> ./test
git diff --check
```

Results: Compose interpolation accepted; 23 Python policy/evidence/image/secret tests
passed; the Backend SIGTERM/SQLite test passed; the Windows test binary
cross-compiled; whitespace validation passed.

## Host acceptance gate

The current worker sandbox cannot connect to `/var/run/docker.sock`
(`permission denied`), non-interactive elevated access is unavailable, and
Windows process interop fails before Docker starts (`UtilBindVsockAnyPort`). No
live crash/restart/down-up/Docker-restart result is claimed from this host.

Run the commands in `compose-application-stack.md` on the designated Windows
Test computer and attach the generated evidence before checking the final
persistence/recovery acceptance item or promoting Ticket 08 to done. This is an
infrastructure evidence gate, not an implementation omission; the harness and
success criteria are committed and reproducible.
