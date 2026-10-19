# T15 fixture migration and local business acceptance

The authoritative scope is the source checkout's
`.scratch/windows-oidc-release/issues/15-data-and-business-acceptance.md`.
Do not mark that issue or the scratch README complete until the actual runner
returns `passed` and sanitized evidence is exported. Worker unit tests and an
auxiliary build are preparation, not live migration evidence.

The source is the healthy `stock-ai-t10-cert` project. Its deleted original
worktree must **never** be reconstructed or used to recreate/restart its
containers. Source export pauses all running project containers, reads the
three actual named mounts, and always unpauses them. It never invokes source
Compose. Matching source master and Account encryption keys come from the
private `local-test-rescue` directory; they are not printed. Keep source API,
OIDC and cookie keys separate from Release.

## Operator command

The tested auxiliary image is approved in
[`auxiliary-images.json`](../../deploy/local-release/auxiliary-images.json).
The five product digests (including middleware and Nginx) come from the
promotion package; the fixture IdP is the only auxiliary image. To rebuild this
auxiliary alone after a reviewed fixture change:

```sh
python3 scripts/local-release/build_fixture.py
```

The builder runs fixture tests, builds only `scripts/local-test/Dockerfile`,
pushes to the existing loopback registry, obtains and pulls the immutable
digest, and updates the auxiliary approval with exact source/test hashes.
An unsuccessful build produces no fabricated approval. Do not independently
run `prepare.py` on the old Release profile: TLS SAN/config changes must be
applied inside the complete snapshot/rollback boundary.

Run from the reviewed checkout with Docker access, as the runtime owner:

```sh
python3 scripts/local-release/accept.py \
  --runtime /home/car/.agentcraft/runtime/proj/local-release \
  --deployment-root /home/car/.agentcraft/runtime/proj/local-release/deployment \
  --evidence-dir /home/car/.agentcraft/runtime/proj/local-release/evidence/t15 \
  --source-secrets /home/car/.agentcraft/runtime/proj/local-test-rescue
```

This command causes brief **Release-only** outages for network/config/key
recreation and rollback rehearsals. It synchronizes reviewed `deploy/` and
`scripts/` into the persistent bundle, including the certified promotion
package and fixture source bytes used by approval verification. No live bind
references a worker checkout. Test and other projects stay intact.

The runner first verifies unchanged certified product digests, healthy
incumbents and persistent mounts, then pauses the target and authenticates a
complete AES-GCM snapshot of all stores, nine keys, TLS, fixture configuration,
env and deployment configuration. The independent recovery key never enters
the export. Source database/History/key export is also encrypted using that
private recovery channel; plaintext staging and browser material live only in
temporary private runtime directories and are removed at completion.

Before importing data, the runner installs the local market profile and
deliberately restores the entire pre-deployment profile. This real rehearsal
checks old data/History, all keys, old TLS/config/env, old auxiliary digest and
healthy original services. It then reapplies the reviewed profile. Migration
adds a second deliberate post-transfer failed checkpoint, restoring and
comparing every previous store and both encryption keys before restarting.

Replacing read-only `0440` keys happens only after target writers stop. The
runner temporarily uses `0600`, writes/truncates/flushes/fsyncs the same inode,
and restores its original mode in `finally`; UID/GID/inode remain unchanged.
All consumers are subsequently recreated. Write and fsync failure regressions
verify restored permissions and a successful retry of original key bytes.

## Strict local market fixture

Release's private application network is `internal: true`. Its fixture has
the alias `api.binance.com` and a TLS listener on container port 443. The
fresh local certificate covers both `app.localrelease` and `api.binance.com`.
The immutable Backend keeps its hardcoded HTTPS Binance URL and existing
`SSL_CERT_FILE`; certificate verification remains enabled. No proxy, insecure
TLS flag or native market call is used. A target-only `compose down` without
`-v`, followed by `up --no-build --pull never`, recreates the actual network
property while preserving all three volumes.

The fixture returns deterministic BTCUSDT price `100`, market rules and
12-field Binance candles. Other symbols are rejected. Private counters at
`http://fake-provider.localrelease:18080/__fixture/market` identify the
fixture, routes, counts, symbol and price. These counters have no public
Nginx route. Acceptance inspects actual Docker network `Internal`, the actual
fixture alias, Backend trust/proxy settings and every running image digest.
Public cookie-authenticated ticker/info/OHLCV requests must match expected
responses and advance **all three** private route counters. A configured YAML
or Provider's generated text cannot satisfy this gate.

## Data and business evidence

Before adaptation, every database passes SQLite integrity and foreign-key
checks. All table counts, primary-key digests, complete row digests,
representative row digests, SQLite sequence digests, and Agent History count
and digest must match. Raw IDs/records and secret values are absent from the
evidence. Every imported Provider ciphertext authenticates under its original
matching master key before any fixture credential replacement.

The public business gate establishes the Release fixture cookie session,
disables all other Providers including external harnesses, changes the local
Provider URL and credential to Release's local fixture while preserving
Provider/token IDs, verifies generation, selects its migrated model, verifies
the migrated Account token decrypts, and creates a fresh acceptance Account
and Session. The deterministic Provider instructs the Agent to read the
market, place a limit buy with stop loss `90`/take profit `110`, then finish.
Acceptance requires a real persisted Backend order with the expected market,
product, side/type, quantity/price, stops and Session source. It also reads
back a separately placed Account-authenticated simulated order with stops,
stops the Session, logs out, and verifies HTTP 401 for the cleared session.
No external Provider installation, account, credential or login is requested.

## Agent service identity (adapter removed)

The first real run passed encrypted migration, rollback, strict local market
provenance, Provider generation and Account checks, then failed Session
creation with HTTP 502, because the then-current Backend refused the Agent's
static service credential once static USER authentication was disabled. The
runner restored the full original T14 profile and all seven healthy services.
This was a failed acceptance, not a completed release. A later attempt stopped at
source approval before runtime mutation; editing approved fixture bytes requires
a fresh auxiliary build and approval.

That was worked around with a fixture adapter on port 18081 that exchanged the
Agent's service secret for a signed token. The adapter has been removed. The
Backend no longer verifies OIDC and accepts the Agent's own `backend_user_token`
(`AGENT_SERVICE_TOKEN`) directly for `GET /v1/accounts/:id`, so the Agent's
`AGENT_SERVER_BACKEND_URL` is `http://backend:7794`. Account-authenticated tools
retain their original Account bearer. Acceptance still requires private
per-route counters proving USER account lookup and Account market/order calls,
with original Account credentials preserved. The T15 evidence on record was
produced with the adapter and predates this change.

Any smoke/migration failure triggers full pre-deployment rollback, including
old auxiliary/config/CA/env, stores and encryption keys. Evidence distinguishes
failure from a successful rollback. After success, the runner writes updated
promotion/Compose/env fingerprints and an authenticated complete accepted
snapshot for T16. The original encrypted snapshot remains available.

For explicit complete-profile recovery, read the private backup path from
`evidence/t15/predeployment-backup.json` and run the persistent helper:

```sh
python3 /home/car/.agentcraft/runtime/proj/local-release/deployment/scripts/local-release/accept.py \
  --runtime /home/car/.agentcraft/runtime/proj/local-release \
  --deployment-root /home/car/.agentcraft/runtime/proj/local-release/deployment \
  --evidence-dir /home/car/.agentcraft/runtime/proj/local-release/evidence/t15-recovery \
  --restore-backup /absolute/path/from/predeployment-backup.json
```

Use the complete-profile helper for these snapshots; the old four-service
T13 restore schema is different. Do not use image-only operator rollback to
restore migrated data or CA/config changes.

Export only sanitized `acceptance.json`, `migration.json`, `runtime.json`,
`ingress.json`, backup metadata, and fingerprint metadata to
`deploy/evidence/local-release/t15/`, with `SHA256SUMS`. Never export archives,
private env/material, browser bodies/cookies, recovery keys or credentials.
Public DNS/TLS, real IdP behavior and native Providers remain outside this
deterministic local evidence.

After the live operator returns success, export and validate offline:

```sh
python3 scripts/local-release/export_acceptance.py \
  --source /home/car/.agentcraft/runtime/proj/local-release/evidence/t15
python3 scripts/local-release/validate_acceptance.py \
  --evidence-dir deploy/evidence/local-release/t15
```

The exporter reads exactly seven named JSON evidence files and validates their
cross-record gates before writing them. It generates checksums and revalidates
the export. Missing checks, unchanged market counters, differing rows/IDs,
unverified keys/snapshots, substituted digests, failed rollback or embedded
secret fields reject completion. It never traverses or copies the runtime,
archives, env, certificates or secret directories.


## Accepted local result

The operator run completed successfully at `2026-10-14T15:32:13.739588+00:00`
with exit 0. The exported seven-file evidence package and `SHA256SUMS` pass
`validate_acceptance.py`; its records are the authoritative detailed result.

All three source stores and Agent History matched their transferred copies by
counts, schema, primary-key hashes and complete row/file hashes before target
writers resumed. Nine stored Provider credentials authenticated with the
matching migrated LLM key, and the Account key was transferred consistently.
Both deliberate rollback checkpoints passed: the complete old profile
(data, History, nine secrets, TLS/JWKS/config/env/Compose and healthy services),
and the data/key checkpoint after transfer. The first Session-auth failure
also exercised successful automatic complete-profile recovery; it is retained
as a failed attempt in the private operator evidence archive.

The accepted public flow established the fixture session, verified generation,
decrypted the migrated Account token, created an Account and Session, completed
an Agent run with a persisted order, verified simulated stop-loss 90 and
take-profit 110, stopped the Session and logged out. Strict-TLS market calls
returned deterministic BTCUSDT price 100 with advancing ticker, information
and OHLCV route counters. The application network is internal, the adapter
cannot be reached on its edge interface, and its observed USER lookup and
Account-authenticated tool routes retain separate authorization semantics.
Actual Backend requests using anonymous, static USER, expired, wrong issuer,
wrong audience and wrong subject credentials all returned 401.

The accepted auxiliary fixture digest is
`sha256:f642bdb97a92a89ae0e878c638b1e51506db70ce9875d83219b98e1921f9cdb1`.
All five T10 product image digests remain unchanged. The accepted complete
AES-256-GCM snapshot has SHA-256
`92fba17f8502d381f9e7c82e61a57c5a6742f525e2dee386bdb2f20ce3ac46f2`;
its authenticated inventory and updated rollback fingerprints are recorded in
the evidence. All seven Release containers were healthy after completion.
The 28 local-release Python tests, 110 repository Python tests and fixture Go
suite passed. T16 final security, restore and soak sign-off remains pending.
