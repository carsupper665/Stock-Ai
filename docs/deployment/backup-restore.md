# Local backup, restore, retention, and rollback

The authoritative recovery path is `scripts/linux_recovery.py` on WSL/Linux
with Docker Desktop in Linux-container mode. It creates a consistent encrypted
set containing all three SQLite databases, Agent History, the Compose file,
the environment file, and these nine namespace-scoped secrets:

- Backend middleware token, Agent service token (`backend_user_token`), and Account-token encryption key
- Agent admin token
- LLM admin token, runtime token, and matching Provider master key
- OIDC client secret and cookie key (the middleware)

The Provider database and master key are one recovery unit. Never restore one
without the other. The recovery key is separate from the nine application
secrets and is never included in a backup.

## Prerequisites and permissions

The workflow requires Python 3 with `cryptography`, Docker Compose, and a local
immutable helper image reference. The helper must be supplied as either
`sha256:<64 hex>` or `repository@sha256:<64 hex>`; tags are rejected for
restore and permission preparation. Pre-stage the documented default helper or
pass a security-approved full local image ID with `--helper-image`.

Keep the environment file, recovery key, backup directory, and recovery
directories private to the operator:

| Object | Required Linux policy |
|---|---|
| Environment and recovery-key files | operator-owned, no group/other access |
| Backup/recovery/secrets directories | `0700` |
| Encrypted backup, checksum, metadata | `0600` while operational |
| Six Compose secret files | `uid 65532`, operator primary GID, `0440`, inside the `0700` directory |
| Restored volume directories/files | `65532:65532`, `0700`/`0600` before first start |

The split secret ownership is necessary for Linux file-backed Compose secrets:
the application image runs as uid `65532`, while the operator still needs
group read access to back up the files. Directory `0700` prevents other group
members from traversing the host directory. Prepare and verify the nine files
without exposing their values:

```bash
python3 scripts/linux_recovery.py prepare-secrets \
  --env-file /srv/stock-ai/test/compose.env \
  --helper-image sha256:REPLACE_WITH_APPROVED_64_HEX_IMAGE_ID
```

The command rejects missing files, symlinks, unexpected names, mutable helper
references, or files accessible to `other`. It reports only counts and modes.
Run the Ticket 02 production-secret value/isolation validation before this
container-readable permission transition.
Keep the recovery key as canonical base64 for exactly 32 random bytes and mode
`0600`; escrow a copy separately from the Docker host and backups.

## Consistent encrypted backup and verification

Backup validates all nine secrets and Compose metadata, requires every
application image reference to be either a full local `sha256:<64 hex>` ID or
`repository@sha256:<64 hex>` digest, records those exact references, stops the
stack, and copies `/data` from the stopped containers. Mutable tags are rejected
before the stack is stopped. Restore revalidates the archived references and
requires the rendered restored Compose configuration to match them exactly.
It restarts the stack only when it was running before the operation. Stopping
all services means each SQLite main file and any WAL/journal state are copied
at one consistent recovery point rather than from an actively mutating file.

```bash
python3 scripts/linux_recovery.py backup \
  --env-file /srv/stock-ai/test/compose.env \
  --compose-file deploy/compose/compose.yaml \
  --key-file /srv/stock-ai/recovery/recovery.key \
  --evidence-file /srv/stock-ai/recovery/evidence/backup.json

BACKUP=/srv/stock-ai/test/backups/stock-ai-test-YYYYMMDDTHHMMSSZ.sai-backup
python3 scripts/linux_recovery.py verify \
  --backup-file "$BACKUP" \
  --key-file /srv/stock-ai/recovery/recovery.key \
  --evidence-file /srv/stock-ai/recovery/evidence/verify.json
```

The archive uses AES-256-GCM with a fresh nonce. A `*.sha256` sidecar detects
transfer/storage corruption before decryption, and the authenticated archive
contains a per-file SHA-256 inventory. `verify` decrypts only into a private
temporary directory, performs safe extraction, checks all required content,
runs `PRAGMA integrity_check`, and reports schema hashes and table counts. It
also records a whole-history fingerprint and file count. Temporary plaintext
is deleted at exit. Command failures are nonzero and diagnostics never include
secret or environment values.

## Safe retention

Keep at least one independently verified off-host copy of both the encrypted
file and its checksum. Retention refuses to delete anything unless a mode-0600
attestation names a local set, matches its SHA-256, and is no more than seven
days old:

```json
{
  "backup_file": "stock-ai-test-YYYYMMDDTHHMMSSZ.sai-backup",
  "sha256": "64-lowercase-hex-characters",
  "verified_at_utc": "RFC3339 timestamp with timezone"
}
```

Create that marker only after independently checking the off-host bytes and
sidecar. Preview, review, then apply the default 14-daily/12-monthly policy:

```bash
python3 scripts/linux_recovery.py prune \
  --backup-directory /srv/stock-ai/test/backups \
  --offhost-marker /srv/stock-ai/test/backups/offhost-verification.json \
  --keep-daily 14 --keep-monthly 12 --dry-run

python3 scripts/linux_recovery.py prune \
  --backup-directory /srv/stock-ai/test/backups \
  --offhost-marker /srv/stock-ai/test/backups/offhost-verification.json \
  --keep-daily 14 --keep-monthly 12
```

Every candidate checksum is verified before any deletion. Noncanonical names,
links, missing sidecars, bad hashes, stale/future attestations, and nonpositive
retention counts fail closed. The attested set is always retained. Only a
backup and its matching sidecar are removed; unrelated files are untouched.
Recovery logs older than 90 days may be removed by the same policy.

Run backup daily and prune only after the replication job has written a fresh
attestation. A systemd user timer or host scheduler must preserve the nonzero
exit status and alert on failure. Store logs in an operator-only directory and
never enable shell tracing around these commands.

## Restore only to new disposable volumes

Restore never writes over a deployed volume. The destination stack name must
match `*-restore-<4-to-16-character-suffix>`, differ from the source stack, and
all three destination volume names must not exist. The workflow creates and
labels the volumes itself, then copies the authenticated set and verifies every
entry is owned by `65532:65532` with directories `0700` and files `0600`.
Failure removes only volumes created by that failed attempt.

```bash
python3 scripts/linux_recovery.py restore \
  --backup-file "$BACKUP" \
  --key-file /srv/stock-ai/recovery/recovery.key \
  --restore-stack stock-ai-test-restore-dr01 \
  --restore-directory /srv/stock-ai/recovery/restore-dr01 \
  --helper-image sha256:REPLACE_WITH_APPROVED_64_HEX_IMAGE_ID \
  --evidence-file /srv/stock-ai/recovery/evidence/restore.json
```

The new recovery directory contains `compose.yaml`, rewritten `compose.env`,
the nine restored secrets, and sanitized restore metadata. The environment
points only to the new stack and recovery secret directory; image references
must equal those in the backup. Review these metadata, then create the stack
and capture a stopped pre-start validation before starting:

```bash
docker compose \
  --env-file /srv/stock-ai/recovery/restore-dr01/compose.env \
  -f /srv/stock-ai/recovery/restore-dr01/compose.yaml create

docker compose \
  --env-file /srv/stock-ai/recovery/restore-dr01/compose.env \
  -f /srv/stock-ai/recovery/restore-dr01/compose.yaml \
  up -d --wait --wait-timeout 180
```

Acceptance requires both phases:

1. Before first start, all three database integrity results, schema hashes,
   table counts, and the Agent History fingerprint equal `verify` evidence.
2. After start, every service is `running/healthy`, baseline rows remain,
   Account, Session, Run History, Message, and Model fixtures are readable, and
   Generate succeeds through the restored encrypted Provider credential.

Agent startup may legitimately append a `server_recovery` Run to the restored
history shard. Therefore compare exact stopped state before start; after start,
verify the baseline Run History through its API and record the additional
recovery state rather than incorrectly requiring a live database to remain
byte-for-byte unchanged.

Destroy the disposable recovery containers and volumes after acceptance. The
exercise fails unless label-based queries prove that both source and restore
Compose projects have no containers, exact-name queries prove that both local
Provider fixture containers are absent, and all six source/restore named
volumes are absent. Do not delete or repurpose the failed/production source
volumes as part of this workflow.

## Image/config rollback versus full data restore

Ticket 12 `update`/`rollback` is an application rollback. It restores the exact
previous immutable image assignments and validates configuration hashes; it
does not touch a named volume, rewind SQLite, or alter Agent History. Use it
only when the existing schemas are proven backward compatible with the prior
images.

A full data restore is a different decision: stop the affected deployment,
select a verified pre-change recovery set, and restore all databases, history,
configuration, and matching secrets together into new empty volumes. The
application has no automated schema downgrade. Do not combine an old Provider
database with another master key, restore one database independently, or use
image rollback as a substitute for migration rollback.

Stop and choose full restore when any affected schema lacks a proven downgrade,
the migration partially committed, the pre-change set cannot be verified, or
the prior image fails against current data. Preserve the failed volumes until
business acceptance and incident review are complete.

## Timed local exercise and evidence

Run the local deterministic exercise only on a Docker host where it may create
randomly named disposable stacks. It resolves all application/helper tags to
full local image IDs, creates an Account, Message, Model, Session, completed
Run and encrypted Provider token, and executes backup → decrypt/verify → actual
retention → new-volume restore → healthy startup → business checks → cleanup:

```bash
python3 scripts/exercise_linux_recovery.py \
  --evidence-file docs/deployment/ticket-13-local-recovery-evidence.json
```

The accepted exercise met the proposed **24-hour RPO** and **4-hour RTO**. Its
sanitized evidence and checksum are:

- [`ticket-13-local-recovery-evidence.json`](ticket-13-local-recovery-evidence.json)
- [`ticket-13-local-recovery-evidence.json.sha256`](ticket-13-local-recovery-evidence.json.sha256)

RPO is measured from the selected backup recovery point to simulated incident
declaration. RTO starts at declaration and ends only after healthy services,
logical database/history checks, and deterministic business-data validation.

## Legacy Windows workflow (not a gate)

`scripts/recovery.ps1`, `scripts/recovery-policy.psm1`, Windows Task Scheduler,
Pester, and NTFS ACL checks are retained as legacy operator material. They were
**not run** for this Linux/WSL acceptance and are not gates. External host,
IdP, DNS, and TLS recovery are also outside this local application recovery
set and recorded as `not_run_non_gate`; those systems require separate plans.
