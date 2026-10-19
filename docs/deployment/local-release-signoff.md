# WSL/Linux local Release handover (T16)

This release is for the agreed local WSL/Linux scope. It uses an OIDC identity
fixture, fake LLM Provider, and deterministic Binance market fixture. No external IdP, real exchange, external test host,
public DNS/TLS renewal, or Windows reboot is required for this acceptance.

The evidence, image and test counts below were recorded at T16 acceptance,
before the middleware merge (the OIDC Gateway and Console became one
middleware, the Nginx image joined the promoted product images, and the Agent
adapter was removed); rerun the sign-off to refresh them. The architecture
section describes the current topology.

The authoritative result is [summary.json](../../deploy/evidence/local-release/t16/summary.json).
Every public artifact is covered by [SHA256SUMS](../../deploy/evidence/local-release/t16/SHA256SUMS)
and `scripts/local-release/validate_signoff.py`. The final local Git tag is
`local-release-wsl-20261015`; it identifies reviewed configuration, scripts,
documentation and acceptance evidence. It does not imply rebuilding the five
product images: their accepted source revision remains
`906326524f70acf77dcffeff86e9433896830509` in the checksummed
[Test promotion](../../deploy/evidence/local-test/promotion/promotion.json).

Final acceptance completed at **2026-10-14 21:07:54 UTC** (2026-10-15 in
Taiwan). Docker Engine is **29.5.2**, Nginx **1.30.5**; all seven accepted
images have zero High/Critical findings. The final process-crash checkpoint,
post-crash 120-second soak and complete snapshot passed. All **144 Python
tests** passed: 110 repository, 30 Release and four private-material tests.

## Architecture and identity boundaries

Only Nginx publishes `127.0.0.1:8443`. The local application origin is
`https://app.localrelease:8443`. Add `127.0.0.1 app.localrelease` to the client
host's hosts file if needed; host acceptance uses curl `--connect-to` instead.
Nginx serves the bundled frontend directly and proxies `/oauth2`, `/v1`, `/agent`
and `/llm` to the middleware on the edge network; it has no `auth_request` and
holds no token. The middleware is the only login layer and routes private APIs to
Backend, Agent Server and LLM Provider Server on the internal application
network. The fixture belongs to both networks. The application network is
Docker `internal: true`; `api.binance.com` resolves to the fixture there.

Browser login uses authorization code, PKCE, state, nonce and signed tokens.
The issuer is `https://app.localrelease:8443/realms/stock-ai-release`, user
subject `local-release-user`, audience `stock-ai-api-release`. The Backend does
not verify OIDC; it accepts the private `backend_middleware_token` that only the
middleware holds. Browser identity headers are stripped at the middleware.
Account tokens retain their existing scope.

The Agent Server connects directly to `http://backend:7794` with its own
`backend_user_token`, which only authorizes Account lookups. Account-authenticated
tool calls preserve their original credentials, and mutation/monitoring routes
never receive USER elevation. The earlier fixture adapter on 18081 that exchanged
the Agent's service secret for a signed token has been removed.

## Accepted versions and immutable images

[Final summary](../../deploy/evidence/local-release/t16/summary.json) records the
actual Docker engine and Nginx versions, all seven immutable image references,
promotion hash, completion time and accepted backup hash. The five product
images (Backend, Agent Server, LLM Provider Server, middleware and Nginx) match
Test promotion exactly. The only auxiliary image is the identity fixture, approved
separately in [auxiliary-images.json](../../deploy/local-release/auxiliary-images.json);
its approval binds its reviewed source bytes to its digest.

T16 found 43 High/Critical findings in the old Nginx image. Updating to the
pinned official stable Alpine base removed most, but three library findings
remained, so the then-separate local Nginx image updated libexpat, pcre2 and
tiff. That image has since been replaced by the promoted Nginx image built from
`docker/nginx.Dockerfile`. The original failing scan is
retained, and the intermediate candidate's three findings are recorded in
`nginx-update-history.json`; only `t16/scans/summary.json` certifies the accepted set.
There are no severity exceptions or ignored unfixed findings in this gate.

T15's evidence remains the original accepted record from commit `34c5b1d`.
`historical-t15-audit.json` verifies its bytes and auxiliary approval against
that Git revision, then validates every T15 gate. The default T15 validator
compares against the current auxiliary approval, so it rejects the historical
Nginx digest after this update. T16 validates the current live image set; the
historical T15 record is not rewritten to claim it tested the new Nginx.

The actual Nginx update took a complete encrypted preupdate snapshot, replaced
only Nginx, then verified all seven services and certificate-verified login.
Its private operational record is `$runtime/evidence/t16/nginx-update.json`.
The public zero-finding reports cover the five products and both auxiliaries.

## Security and recovery acceptance

- `security.json`: anonymous, forged bearer, spoofed headers, wrong subject,
  wrong issuer/audience, expired ID/access token and expired session cannot
  access private APIs. A second browser receives a copy of the original
  transaction cookie and reuses the already redeemed callback; its private
  requests fail. The legitimate browser succeeds. Callback URLs, codes,
  state, tokens and cookie values are never exported.
- `security.json` and `rate-limit.json`: strict local TLS, same-origin login,
  Secure/HttpOnly/SameSite `__Host-` cookies, CSP/HSTS/frame/referrer/nosniff
  headers, hidden server version, oversized-body HTTP 413 and login HTTP 429.
- `surface.json`: host TCP listeners, private loopback port probes, sole
  Release publication, persistent bind paths, private adapter edge denial,
  and forbidden artifact/control probes. A generic SPA fallback is identified
  as HTML, not mistaken for a downloadable database or control endpoint.
  Existing Foreman, registry and legacy Test listeners are outside this scope.
- `incidents.json`: SIGKILL of Backend triggers restart of the same container
  and advances Docker's automatic restart count, restoring its API. The signal
  is sent to the inspected application PID by a networkless immutable helper;
  `docker kill` is avoided because Docker treats it as a manual stop. A temporary IdP fixture outage blocks fresh login and
  private APIs; removing the outage restores login. Pausing the shared fixture
  makes Provider generation fail or time out, and unpausing restores it. That
  last probe also affects collocated identity, market and adapter services.
- `backup.json` and `restore.json`: fresh authenticated AES-256-GCM backup,
  complete inventory, all three databases and History restored into **new**
  disposable volumes. Every row, schema, ID and History hash matches before
  writers start. All Provider ciphertexts authenticate; restored login,
  account token decryption and Provider generation succeed. Seven restored
  services become healthy. Clone containers, networks and volumes are removed;
  live Release volumes are not replaced by this rehearsal.
- `soak.json` and `accepted-backup.json`: at least 120 seconds of healthy
  services and deterministic market reads, at most 2 MiB log growth per
  container, no known runtime secret in logs, and a final consistent snapshot
  whose SQLite integrity checks pass. This is a bounded local check.

## Persistent runtime and operator commands

The owner is Car. Runtime, nine secrets, local TLS/JWKS and fixture configuration,
operator bundle, encrypted backups and recovery key are under
`/home/car/.agentcraft/runtime/proj/local-release`. Runtime/backups are mode
0700; env/recovery key are 0600; mounted private material is read-only. No live
bind source is under a worker worktree. Keep an independent protected copy of
the recovery key and encrypted backup off the runtime disk; local acceptance
does not claim an off-host copy was made.

From `/home/car/wsl/proj`, repeat the real gate (it briefly pauses services for
snapshots and intentionally injects the documented local faults):

```bash
runtime=/home/car/.agentcraft/runtime/proj/local-release
rtk python3 scripts/local-release/scan_release.py --output "$runtime/evidence/recheck/scans"
rtk python3 scripts/local-release/signoff.py --runtime "$runtime" --output "$runtime/evidence/recheck"
rtk python3 scripts/local-release/validate_signoff.py "$runtime/evidence/recheck"
```

Use the persistent reviewed bundle when a checkout/worktree is unavailable:

```bash
runtime=/home/car/.agentcraft/runtime/proj/local-release
bundle="$runtime/deployment"
rtk python3 "$bundle/scripts/local_operations.py" status \
  --env-file "$runtime/compose.env" \
  -f "$bundle/deploy/compose/compose.yaml" \
  -f "$bundle/deploy/compose/compose.local-release.yaml" \
  --evidence-dir "$runtime/evidence/status" --backup-dir "$runtime/backups"
```

For a planned container restart, use the same env and two Compose files with
`docker compose restart --no-deps backend`, then check health and login. Avoid
recreating the legacy Test source: its original worktree-mounted configuration
has disappeared. Release has persistent configuration and can be recreated
from its reviewed bundle using `up -d --no-build --pull never --wait`.

## Complete backup restore, updates and rollback

Keep the accepted backup's `.sha256` sidecar and the matching recovery key.
`signoff.py` creates and authenticates fresh complete backups and rehearses
restoration. It verifies encrypted inventory before reading restored data.
Do not use the older four-service T13 helper for the seven-service complete
profile schema.

For deliberate disaster recovery of the live Release profile, select a verified
complete archive from the `backup_file` in an accepted backup report:

```bash
runtime=/home/car/.agentcraft/runtime/proj/local-release
rtk python3 "$runtime/deployment/scripts/local-release/recover_profile.py" \
  --runtime "$runtime" --backup /path/to/selected-complete.sai-backup \
  --evidence-dir "$runtime/evidence/recovery"
```

This command authenticates the selected archive and checks that all immutable
images are locally available before mutation. It takes a fresh incumbent
snapshot, stops only Release, then restores data/History and matching keys,
TLS/JWKS/configuration, env and deployment configuration together. A failed
restore attempts to restore the incumbent snapshot. This live recovery command
is supplied for operation; T16 exercises the independent-volume rehearsal and
T15 exercised actual failed-checkpoint full-profile rollback.

The existing product update/image rollback workflow is documented in
[local-operations.md](local-operations.md). It requires the checksummed promoted
package and fresh metadata. Image rollback alone does not undo data, TLS,
secrets or auxiliary configuration. Use the complete-profile archive for those.
The final T16 `rollback.json` reflects the updated auxiliary hash and env, not
the historical T14/T15 metadata. Copy it to persistent operational evidence
before using it. Retain prior images and backups until a new release passes.

An Nginx update is now an ordinary product image update: promote a new package
and use the update workflow in [local-operations.md](local-operations.md). There
is no separate auxiliary Nginx updater. Re-run full sign-off and issue a new
local tag after accepting changes.

## Incident response and remaining local limitations

Car owns lifecycle, backup custody and release acceptance. On repeated health
failure, inspect sanitized operator diagnostics, preserve encrypted backups,
and stop affected Release services before restore. Restore original data and
matching encryption keys together; never regenerate the master/account key
against existing ciphertext. If identity is unavailable, restore the fixture
first; fresh login must fail closed. Existing valid sessions may continue until
their normal expiry. A Provider outage must be fixed before resuming Agent runs.

The original T11 worktree cleanup removed TLS/JWKS/config while containers were
running. The nine original secrets were rescued, and replacement local keys
were installed. T15's first Agent run exposed static service authentication;
it rolled back safely before the scoped adapter fixed that path. T16's first
runner incorrectly required a single nosniff header while both edge and Console
supplied the same safe value; the predicate now checks all values. These are
recorded incidents, not waived final failures.
An initial `docker kill` rehearsal needed explicit `compose start backend`;
the corrected process-crash checkpoint passed with automatic restart, followed
by another 120-second soak and complete snapshot. `checkpoint_reruns` records
that final sequence. The SPA probe was also tightened to compare fallback bytes
against the real homepage rather than assuming a frontend mount element name.

The local self-signed fixture certificate lasts two days. Check its expiry
before a later session. Renew it with both `app.localrelease` and
`api.binance.com` SANs, preserving all application secrets and signing-key/data
relationships; update the trust certificate and restart the consumers together,
then re-run strict-TLS acceptance. Public certificate renewal is outside scope.
Fixtures simulate trading and generation; no production money or remote IdP
integration was tested. The 120-second soak does not establish long-term
production stability. Legacy Test stacks were left intact and were not
recertified as part of T16.
