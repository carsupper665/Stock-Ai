# Backup, restore, and rollback

`scripts/recovery.ps1` manages encrypted recovery sets for the isolated Compose
stack. It is designed for Windows PowerShell 7 and Docker Desktop in Linux
container mode. Backups cover all three SQLite databases, the entire Agent
History directory, the Compose definition and environment file, and all six
mounted secrets (including the matching LLM master key). The LLM database and
master key must always be restored from the same set.

## Setup and backup

Create an RSA recovery certificate in `LocalMachine\My`; protect and escrow its
private key separately from the Docker host and grant the operator account read
access. The certificate thumbprint and private key are required to verify or
restore. Keep the backup directory on an encrypted volume with an ACL limited
to the recovery operator and backup administrators. Do not include the private
key in any recovery set.

For example, create the directory on a BitLocker-protected data volume and
remove inherited access before granting the dedicated operator, SYSTEM, and
local Administrators access. Run elevated and replace the account name:

```powershell
$BackupDir = 'D:\Stock AI Recovery\Release'
$BackupAccount = 'HOSTNAME\stock-ai-backup'
New-Item -ItemType Directory -Path $BackupDir -Force | Out-Null
icacls $BackupDir /inheritance:r /grant:r "$($BackupAccount):(OI)(CI)F" 'SYSTEM:(OI)(CI)F' 'Administrators:(OI)(CI)F'
```

Set `STOCK_AI_BACKUP_DIR` in the environment file. Run these commands from an
elevated PowerShell 7 session after confirming the recovery certificate and
its private-key escrow:

```powershell
$Ops = 'C:\Program Files\Stock AI\scripts\recovery.ps1'
$EnvFile = 'C:\ProgramData\Stock AI\Release\compose.env'
$Cert = 'REPLACE_WITH_CERTIFICATE_THUMBPRINT'
& $Ops -Action Backup -EnvFile $EnvFile -CertificateThumbprint $Cert
$Backup = Get-ChildItem 'C:\ProgramData\Stock AI\Release\backups\*.sai-backup' | Sort-Object Name -Descending | Select-Object -First 1
& $Ops -Action Verify -EnvFile $EnvFile -BackupFile $Backup.FullName -CertificateThumbprint $Cert
```

Backup gracefully stops the stack before copying volume data from the stopped
containers, so SQLite main files and WAL state are captured consistently. It
restarts the stack if it was running before the operation. The output is a
timestamped AES-256-GCM authenticated envelope; its data key is wrapped with
RSA-OAEP-SHA256 for the recovery certificate. A SHA-256 sidecar detects
transfer/storage damage before decryption, and an internal per-file SHA-256
inventory is checked after decryption. `Verify` decrypts into a temporary
directory, checks the inventory and required files, then removes its temporary
plaintext. Recovery logs contain operation, file, timing, and failure type,
never secret values. Daily `recovery-YYYYMMDD.log` files are retained for 90
days; Task Scheduler's Last Run Result remains the immediate failure signal.

The sample retention policy is 14 most recent daily sets plus one set from
each of the 12 most recent months. Set local/off-host retention to match legal
and business requirements; keep at least one verified copy off-host. Run
`-Action Prune` only after off-host replication has verified both the encrypted
file and sidecar:

```powershell
$BackupFile = Get-ChildItem 'D:\Stock AI Recovery\Release\*.sai-backup' | Sort-Object Name -Descending | Select-Object -First 1
$Hash = (Get-FileHash -LiteralPath $BackupFile.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
# Only write this attestation after the off-host copy and its sidecar were independently verified.
@{ backup_file = $BackupFile.Name; sha256 = $Hash; verified_at_utc = [DateTime]::UtcNow.ToString('o') } |
  ConvertTo-Json | Set-Content 'D:\Stock AI Recovery\Release\offhost-verification.json' -Encoding utf8NoBOM
& $Ops -Action Prune -EnvFile $EnvFile -OffHostVerificationMarker 'D:\Stock AI Recovery\Release\offhost-verification.json' -KeepDaily 14 -KeepMonthly 12
```

Prune refuses to run without this marker or if it is older than seven days,
references a missing set, or has a different local encrypted-file hash. The
marker is an operator attestation; the copy/remote hash check remains part of
the off-host replication procedure.

Register the daily backup and weekly retention jobs under a dedicated Windows
account after confirming its Docker access and off-host replication schedule:

```powershell
& $Ops -Action InstallSchedule -EnvFile $EnvFile -ScheduledTaskUser 'HOSTNAME\stock-ai-backup' -CertificateThumbprint $Cert
```

The installer prompts for the account password and stores it through Task
Scheduler; it registers daily backup at 02:00 and weekly prune at Sunday 04:00.
Run the retention task only after off-host replication verification. Run the
operation as a dedicated, non-interactive Windows account and inspect the Task
Scheduler Last Run Result and the current `recovery-YYYYMMDD.log`. A failed run exits
nonzero and appends a visible `FAIL` record. Docker Desktop's daemon access is
effectively administrative: adding an account to `docker-users` grants control
over the host. Therefore the deployment owner must explicitly approve this
privilege or provide a separately constrained backup agent before enabling a
scheduled task; this ticket does not claim the account is least-privileged
while it can control the Docker daemon. Do not schedule a task that hides this
boundary. The scheduled task must run Backup, verify replication, then Prune;
configure Task Scheduler to retry failures and retain task history. The weekly
prune task checks the same marker and fails visibly when off-host verification
is stale.

## Restore into an empty stack

First verify the chosen set. Prepare the exact environment and image references
from that backup on the recovery host, with the named volumes newly created and
empty. Restore refuses existing data, with the allowlisted image-owned empty
scaffolding described below as the only exception. It stops the stack,
creates containers without starting services, copies the complete
service state into the empty named volumes, and exports the archived Compose
file, environment file, and secrets to a separate recovery directory. The
exported environment file is rewritten to point at its exported secrets.

Fresh volumes may contain only the Docker image's empty copy-up scaffolding:
`backend:/data/logs` and `agent-server:/data/history`. Those exact directories
must be empty; the LLM Provider volume must contain nothing. Any file, populated
scaffold, symlink, database, or unknown path blocks restore. After copying each
volume, restore runs the immutable helper image
`golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079`
with no network, a read-only container root, `CHOWN` plus `DAC_OVERRIDE`
capabilities for the ownership pass, and `DAC_OVERRIDE` for the read-only
ownership verification. It
rejects symlinks, sets every `/data` entry to `65532:65532`, then verifies every
entry using a separate read-only mount. This matches the service UID/GID in
the application images; missing helper image or any chown/verification error
aborts restore.

Before decrypted content is extracted, and again before secrets are written to
`RestoreDirectory`, the script protects the directory DACL against inheritance,
sets the operator as directory owner, then verifies that only the current
operator, SYSTEM, and local Administrators have Full Control. It applies and
verifies the same ACL to the `secrets` child
before copying any of the six secret files. Any unexpected principal or ACL
failure aborts before secret files are written.

The fixture regressions are in `scripts/test/RecoveryPolicy.Tests.ps1`. They
exercise allowed empty scaffolding, populated/unknown data rejection, ACL
allow/deny behavior, and successful/failing UID/GID restoration command paths.
They require Pester on Windows; this Linux worktree does not have PowerShell and
the prior operator instruction prohibits attempting it here. Run them on the
Windows Test host with:

```powershell
Invoke-Pester -Path 'scripts/test/RecoveryPolicy.Tests.ps1' -CI
```

```powershell
$Recovery = 'C:\ProgramData\Stock AI\Release\restore-20261013'
& $Ops -Action Restore -EnvFile $EnvFile -BackupFile 'C:\ProgramData\Stock AI\Release\backups\stock-ai-release-20261013T120000Z.sai-backup' -RestoreDirectory $Recovery -CertificateThumbprint $Cert
```

Before starting, review the archived config and image digests, secret ACLs,
certificate/issuer availability, and environment identity. Start with the
recovered environment and run the operator `Status`/`Diagnose` checks. Confirm
accounts and sessions, Agent run history and messages, available models, and
that an authorized Provider credential can be decrypted. Do not point the
recovered deployment at a different environment's identity provider or
secrets. Keep the failed/old volumes untouched until business acceptance.

## Image/config rollback and database migration limits

The Ticket 12 `Update`/`Rollback` actions restore immutable image references
and deployment metadata only. They intentionally do not change any named
volume or restore a database snapshot. A database migration may be
irreversible or incompatible with the prior image; changing the image alone
does not undo a migration. Before an update, require a verified full backup
and identify whether every service's schema is backward compatible. If not,
stop the stack and choose a full-set restore into empty volumes with the exact
matching config, image digests, and secrets. Never combine an old Provider DB
with a rotated master key, or restore one database independently while
leaving the others at a later point.

The current deployment stores SQLite and does not provide an automated schema
downgrade. PostgreSQL, external IdP state, TLS private keys/certificates, and
off-host services are not included in this application backup and require
their own coordinated recovery plans. A migration decision must stop if its
pre-migration backup is absent/unverified, if a downgrade has not been proven
for every affected schema, if the matching key/config is unavailable, or if
the intended RPO/RTO cannot be met.

## Timed recovery exercise

Proposed targets pending Data Owner approval: **RPO 24 hours** (daily backup)
and **RTO 4 hours** for the complete application recovery. Record the selected
backup timestamp as the recovery point; compare it with the declared loss
time. Measure RTO from incident declaration through healthy services and
verified sample business data, not merely through file extraction. Exercise
the empty-stack procedure on an isolated Test host with representative account,
session, run, message, model, and encrypted Provider credential records. Save
sanitized timings and verification results in a dated evidence file. A timing
exercise without restored data checks is not a recovery pass.

The current Linux worktree environment has no PowerShell runtime and its
process cannot access Docker Engine. Therefore no real restore, RPO/RTO result,
Windows scheduled-task result, or Windows retention execution is claimed by
this change. The blocked probe record is
[`ticket-13-exercise-blocked.json`](ticket-13-exercise-blocked.json). The
exercise must be run and recorded on the isolated Windows Test host before
Release deployment.
