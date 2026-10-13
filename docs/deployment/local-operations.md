# Local WSL/Linux operator workflows

Use [`scripts/local_operations.py`](../../scripts/local_operations.py) to
preflight, start, inspect, stop, update, roll back, and diagnose the isolated
Compose application from WSL/Linux. It invokes Docker and Compose with argument
arrays, so checkout, env, Compose, evidence, certificate, backup, and promotion
paths may contain spaces. Commands are non-interactive and return nonzero when
a gate fails. The workflow never launches the legacy mixed Windows/Linux
distribution and rejects unreviewed Compose services, build directives, and
host networking.

The current Compose application has the four Ticket 08 services. When the
local Test or Release overlay is available, pass each Compose file with another
`-f` option. Keep the two environments in separate directories with unique
project names, env files, secrets, networks, volumes, and data. Put env files,
secrets, promotion material, and runtime evidence outside Git.

Create a private Test directory, copy the example, replace its placeholder
secret paths and OIDC fixture values with the prepared local values, then lock
the env file before use:

```bash
install -d -m 700 "$HOME/Stock AI/local-test"
cp "$ROOT/deploy/compose/compose.env.example" "$HOME/Stock AI/local-test/compose.env"
chmod 600 "$HOME/Stock AI/local-test/compose.env"
```

## Preflight and lifecycle

For example, these paths contain spaces intentionally:

```bash
ROOT="$PWD"
ENV_FILE="$HOME/Stock AI/local-test/compose.env"
EVIDENCE="$HOME/Stock AI/operator evidence/local-test"
COMPOSE="$ROOT/deploy/compose/compose.yaml"

python3 "$ROOT/scripts/local_operations.py" preflight \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE"
python3 "$ROOT/scripts/local_operations.py" start \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE"
python3 "$ROOT/scripts/local_operations.py" status \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE"
python3 "$ROOT/scripts/local_operations.py" diagnose \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE"
python3 "$ROOT/scripts/local_operations.py" stop \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE"
```

Preflight reports an unavailable or non-Linux Docker engine, fewer than two
CPUs or 4 GiB of engine memory, less than 10 GiB free beside the env file,
unavailable published ports, missing Compose/bind/secret files, unexpected
services, local build directives, host networking, invalid environment, and
mutable Release image references. It records only safe facts in
`preflight.json`; Compose's interpolated config and command output are never
written to evidence or echoed on failure. Test can use temporary tags while
being exercised. Release must use repository references pinned to
`sha256:<64 lowercase hex>`.
The env file must be readable only by its owner (mode `0600` on Linux); the
operator also prevents inherited shell variables from overriding values in
that explicit env file.

Host publication is restricted to Nginx. Backend, Agent Server, LLM Provider
Server, Console, Gateway, and IdP must have no published host ports. Nginx must
bind to `127.0.0.1` or `::1`; wildcard and non-loopback addresses fail closed.
The explicit local ingress contract is Test on loopback ports 80 and 443, and
Release on loopback port 8443. Nginx ports outside that environment's contract
are rejected before the port availability probe.

For Release, preflight and start must receive the checksummed Test promotion
manifest. Preflight validates the promoted application references against it
exactly:

```bash
RELEASE_ENV="$HOME/Stock AI/local-release/compose.env"
RELEASE_EVIDENCE="$HOME/Stock AI/operator evidence/local-release"
PROMOTION="$HOME/Stock AI/promotion/promotion.json"
python3 "$ROOT/scripts/local_operations.py" preflight \
  --env-file "$RELEASE_ENV" -f "$COMPOSE" --evidence-dir "$RELEASE_EVIDENCE" \
  --promotion-manifest "$PROMOTION"
python3 "$ROOT/scripts/local_operations.py" start \
  --env-file "$RELEASE_ENV" -f "$COMPOSE" --evidence-dir "$RELEASE_EVIDENCE" \
  --promotion-manifest "$PROMOTION"
```

Start runs `compose up -d --no-build`, then waits for every configured service
to be running and healthy. On timeout it exits nonzero with the service state
and the path to `diagnostics.json`. Stop sends a 30-second graceful stop in
edge-to-core order (Nginx, Console, Gateway, Agent Server, LLM Provider Server,
Backend, then local IdP where present), and confirms that no project container
is still running. It does not remove volumes.

Status writes a sanitized `status.json` with configured image references and
digest, container health, restart count, writable-container bytes, Docker disk
usage, configured certificate expiry, OIDC discovery reachability, and age of
the newest verified `*.sai-backup` in the optional backup directory. A backup
counts only when its matching `.sha256` sidecar names the file and matches its
SHA-256 bytes; recovery logs, off-host markers, orphan sidecars, and bad-checksum
files do not make backup freshness appear current. Diagnostics omit env,
secrets, logs, and certificate contents. Status also records
`windows_restart_and_ntfs: "not_run"`; those checks are optional in the local
WSL/Linux scope.

## Promoted image update and rollback

Update accepts only `stock-ai.promotion-manifest.v1` with `status: "promoted"`,
`environment: "Test"`, a full Git revision, a configuration schema version,
the declared SHA-256 sidecar trust, and eight passed acceptance results. Its
`images` map contains exactly Backend, Agent Server, LLM Provider Server, and
Console. `test_stack_images` and `artifacts` cover those services plus the OIDC
Gateway, and every `build_digest` must match the corresponding immutable
reference. The verifier checks each SBOM, scan, filesystem inventory, build
provenance, build acceptance, certification JSON, and eight per-result evidence
files against both its manifest hash and its unique `SHA256SUMS` entry. Release
preflight compares configured application image references exactly with the
verified four-image `images` map and, when OIDC Gateway is present, matches it
to the certified Test stack Gateway digest. The command snapshots the old and
new image maps, project, Compose input hash, normalized Compose config hash, non-image
env hash, and promotion hash to a deterministic `rollback-<key>.json` file
before changing image assignments. The normalized fingerprint removes only
the image fields for promoted services; it retains all other Compose topology
and configuration. The env fingerprint hashes parsed non-image env values and
does not store the env file or its values. Rollback compares all three
fingerprints before mutation and fails closed on Compose or non-image env
drift. Update pulls the selected references, starts with
`--no-build`, and waits for health. A failed update restores the old assignments
and waits for the old stack to become healthy; the command still returns
failure so the operator sees the unsuccessful update.

```bash
PROMOTION="$HOME/Stock AI/promotion/promotion.json"
python3 "$ROOT/scripts/local_operations.py" update \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE" \
  --promotion-manifest "$PROMOTION"

python3 "$ROOT/scripts/local_operations.py" rollback \
  --env-file "$ENV_FILE" -f "$COMPOSE" --evidence-dir "$EVIDENCE" \
  --rollback-metadata "$EVIDENCE/rollback-<key>.json"
```

Rollback validates the metadata schema, Compose project, service map, and that
the running configuration still matches the promoted image map recorded by
that update. It reapplies only the recorded previous references, starts without
building, and waits for health. It refuses stale metadata after another image
change. Image rollback does not restore databases or history; use
[`backup-restore.md`](backup-restore.md) for data recovery.

## Evidence and scope

Automated policy and fixture regressions are in
[`scripts/test/test_local_operations.py`](../../scripts/test/test_local_operations.py).
The previous Windows-specific exercise remains documented as historical
evidence in [`ticket-12-windows-evidence.md`](ticket-12-windows-evidence.md).
Windows restart and NTFS checks are not gating for this local scope and are
reported as `not_run` by the Linux operator workflow. Successful Update and
Rollback require a real promoted manifest from Ticket 10; synthetic checksum
fixtures are unit-test data and are not acceptance evidence.
