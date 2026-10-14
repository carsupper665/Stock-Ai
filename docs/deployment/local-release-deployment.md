# Local Release preflight and deployment (T14)

Run from a checkout with the certified T10 package. Runtime, backup, recovery
key, operator metadata and all live bind sources must survive worktree cleanup.
The runner rejects resolved paths under `worktrees` and the current checkout.
Use the persistent bundle for the rollback executable; keep its configuration and
metadata in a persistent bundle. Public DNS/TLS and host reboot are non-gating.

```sh
RUNTIME=/home/car/.agentcraft/runtime/proj/local-release
python3 scripts/local-release/deploy.py \
  --runtime "$RUNTIME" \
  --deployment-root "$RUNTIME/deployment" \
  --backup-dir "$RUNTIME/backups" \
  --key-file "$RUNTIME/recovery.key" \
  --evidence-dir "$RUNTIME/evidence/t14" \
  --operator-script "$RUNTIME/deployment/scripts/local_operations.py"
```

Add `--preflight-only` to check without pausing or recreating existing services.
Create the runtime and backups with mode 0700 and the independent recovery
key with mode 0600, containing canonical base64 for 32 random bytes. Preserve
that recovery key separately from the encrypted backups. Private preparation
is `prepare.py --runtime "$RUNTIME"`; existing valid secrets are preserved.
Never regenerate application keys when encrypted data depends on them.

Before deployment, copy the reviewed `deploy/compose/compose.yaml`,
`deploy/compose/compose.local-release.yaml`, `deploy/nginx/release-nginx.conf`
and `deploy/nginx/release-edge.conf.template` to
`$RUNTIME/deployment/deploy/`, retaining the `compose/` and `nginx/` directories.
All four public configuration files may be mode 0644; runtime parents are 0700.
The copies must match the reviewed checkout bytes. Bind mount paths are rendered
relative to that persistent bundle. Editing a checkout after copying does not
update deployed configuration. Recopying configuration invalidates old rollback
metadata and requires a fresh snapshot and preflight.
Keep `scripts/local_operations.py` and its `scripts/promotion_contract.py`
dependency in the bundle too, preserving their reviewed bytes. The accepted
bundle also contains the promotion package and auxiliary approval for later
verification without a disposable checkout.

The runner verifies the Linux engine, at least two CPUs and 4 GiB memory,
10 GiB free on both runtime and Docker volume filesystems, and no more than
60 seconds of host/engine clock skew. Required files, private material,
identity, persistence, every approved image digest and every bind path are
checked before mutation. Port 8443 must be free or published by the existing
Release Nginx. Existing Release services must match the exact approved digests.
No image pull or build is performed. The configured OIDC fixture and fake
provider are local dependencies; no external account or network service is
required. Their actual reachability is checked during health/login acceptance.

The encrypted snapshot pauses all running Release containers, reads all three
volumes through the approved immutable Nginx helper, then unpauses them even
if backup fails. It preserves databases and their WAL state, Agent History,
all nine secrets, TLS/JWKS/config material, env and the deployment bundle.
All databases pass SQLite integrity checks. AES-256-GCM authentication, sidecar
SHA-256 and the entire decrypted file inventory are checked before Compose
recreation. Existing volumes are retained. Startup uses
`up -d --no-build --pull never --wait` with the two Release Compose files.

`deployment.json` records the non-secret Compose, normalized configuration,
non-image env and promotion fingerprints and the exact rollback command.
`rollback.json` uses the existing operator rollback schema. This T14 deployment
keeps the same certified product digests, so its image rollback reasserts that
same image set; it does not undo TLS/JWKS replacement or restore data. Full
data recovery requires the matching complete encrypted snapshot into new
disposable volumes. The T13 four-service restore helper does not consume the
new complete-profile archive schema; do not pass this archive to it or claim a
complete-profile restore was exercised.

Health acceptance includes Nginx's configuration/TCP probe and all six other
services' healthchecks. Certificate-verified curl proves the complete local
login, JWKS-backed session and authenticated API flow, and rejects anonymous
API access. Docker inspection proves exact running digests, separate Test and
Release binds/networks/volumes and only Nginx published at `127.0.0.1:8443`.
A host socket connects to that ingress. The runner restarts only Backend,
confirms the same container has a changed start timestamp, waits for all
containers to recover, and repeats runtime and login acceptance.

## T11 material-loss recovery

Merging T11 removed its disposable worktree while the Docker stack remained
running. The directory-mounted fixture configuration/private keys disappeared;
the IdP became unhealthy. `docker cp` also failed because Docker could not
reconstruct the deleted bind source. Car rescued all nine application/Gateway/
fixture secrets without displaying them. Preserve those exact files from
`/home/car/.agentcraft/runtime/proj/local-release-rescue`; do not rotate the
master, account-token or cookie keys. The old public certificate remains in
the rescue directory as history, outside the new `certs` directory.

Read-only live volume inspection found zero Backend accounts and zero LLM
Provider credentials, with both SQLite integrity checks passing. The evidence
is `deploy/evidence/local-release/t14/material-loss.json`. This inspection
did not stop or recreate the existing product containers. The lost TLS private
key and fixture signing key require new local keys, as explicitly authorized
by Car. Reconstruct fixture configuration with the same Release identity.
Existing browser sessions may require login again after the fixture signing
key replacement. Product images are unchanged and never rebuilt.

Only sanitized evidence belongs in Git. Operational backups, recovery keys,
env files, cookies, secret values and private keys remain in the persistent
private runtime. Checksum the exported evidence and do not mark T14 complete
until the real runner exits successfully and its deployment/restart evidence
is present. T15 fixture migration and business acceptance are separate gates.

## Accepted local result

Car executed the real runner successfully on 2026-10-14, completing at
14:38:47 UTC. [Sanitized evidence](../../deploy/evidence/local-release/t14/deployment.json)
and [SHA256SUMS](../../deploy/evidence/local-release/t14/SHA256SUMS) are committed
under `deploy/evidence/local-release/t14/`. The preflight recorded 12 CPUs,
33,202,769,920 bytes engine memory, over 987 GB free on the engine volume
filesystem and 0.016 seconds host/engine clock skew. The complete-profile
encrypted snapshot was authenticated and its decrypted inventory verified
before recreation. Its checksum is
`af15c96f132952df68af87bfd1c1cf9de8d80fb0c5d2dca477682a4d2906a000`.

All seven containers, including Nginx, were healthy before and after the
Backend restart. Both actual certificate-verified login checks completed on
the Release origin with four redirects and HTTP 200. Authenticated API access
returned 200 and anonymous access 401. The post-restart check encountered
HTTP 429, waited the existing 12-second retry interval and passed; the actual
status sequence is preserved. Runtime inspection confirmed approved digests,
disjoint Test resources, persistent bind sources and Nginx as the sole
published ingress. No build, image change, data migration or host reboot was
performed. All 128 Python tests passed (110 repository, 14 Release, four private
material). The exact operational rollback command is in `deployment.json` and
references only the persistent bundle/runtime paths.
