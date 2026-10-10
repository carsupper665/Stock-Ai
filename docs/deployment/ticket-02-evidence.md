# Ticket 02 acceptance evidence

**Ticket:** 02 — Enforce production secret and configuration safety  
**Implementation date:** 2026-10-11  
**Security controls:** `SEC-DEPLOY-01`, `SEC-SECRETS-02`

This record maps the authoritative Ticket 02 checklist to tracked,
non-secret evidence. It contains no generated values or environment-specific
host evidence.

| Acceptance item | Evidence |
|---|---|
| Reject missing, placeholder, development, weak, or reused USER/Admin/Runtime/cookie/encryption secrets | `scripts/production_secrets.py` validates the complete namespace and role separation. Backend, Console, Agent, and LLM production loaders independently require `_FILE` sources in the exact environment namespace and reject weak values. Negative cases are in `scripts/test/test_production_secrets.py` and the service loader tests. |
| Generate independent Test and Release values without disclosure | `generate` uses the OS CSPRNG, refuses an existing destination, writes exclusive files, and prints only environment and destination. The generation-silence test compares every value against captured stdout/stderr and proves no Test/Release value is reused. |
| Prevent cross-environment configuration or Docker-secret loading | Environment-prefixed filenames, manifest markers, exact secret/mount allowlists, optional cross-namespace comparison, and production service basename checks fail closed. Tests cover wrong environment, wrong filename, and deliberate value reuse. |
| Validate and preserve the LLM master key | The gate requires canonical base64 for exactly 32 random bytes before startup. `production-config.json` binds the retained Provider database to the preservation rule, generation refuses overwrite, the existing LLM gateway verifies decryptability, and `production-secret-safety.md` defines recovery/rotation requirements. |
| Value-free diagnostics | Diagnostics contain setting, source, boolean validity, and reason category only. Tests assert that no configured value is present. Disclosure scans report only artifact path and pass/fail. |
| Restrict Windows files and container mounts | Generation removes Windows ACL inheritance and grants only the current deployment administrator, Administrators, and SYSTEM. The PowerShell wrapper revalidates every ACL. The manifest validator enforces an exact per-service, read-only `/run/secrets` allowlist; Nginx/frontend receive no secret. |
| Prove non-disclosure from images, frontend, HTTP, errors, and logs | The disclosure test injects a configured sentinel into each artifact class and proves the scan blocks it without echoing it. Existing Backend database-log, Agent continuation/error, and LLM upstream-error tests cover runtime redaction. Tickets 07/09/10 rerun the scanner against real images and captured Test artifacts when those artifacts exist. |
| Protect Account tokens in retained Backend data | Production requires a separate Backend encryption key. AES-GCM ciphertext and a nullable, database-unique HMAC lookup replace plaintext storage while retaining the dev/no-key token invariant; migration compacts old plaintext pages, transactions retain the key, and a wrong retained key fails closed. `account_encryption_test.go` proves migration, duplicate create/rotation rejection, prefix-safe create/reset, raw-file non-disclosure, lookup, and wrong-key rejection. |

## Reproducible checks

The following checks passed in the Linux worktree:

```text
python3 -m unittest discover -s scripts/test -v
# 16 passed

python3 scripts/validate_security_contract.py
# security contract valid: 31 authorization cases, 14 risks

go test ./internal/config ./internal/database ./internal/account ./internal/auth ./internal/console
# 32 passed

go test ./cmd/agent-server ./internal/agent
# passed

go test ./cmd/llm-provider-server ./internal/gateway
# passed
```

All three Go module trees also compile with `go test ./... -run '^$'`. Full
integration suites were attempted; tests that open loopback listeners cannot
run in this sandbox (`socket: operation not permitted`). This is an environment
restriction, not a fabricated host pass. The Windows ACL workflow is implemented
and statically covered here; host execution belongs to the later Windows/Test
acceptance tickets.

## Authoritative checklist update

The `.scratch/windows-oidc-release` path in this worktree is a symbolic link to
the read-only source checkout and is ignored by Git. Worktree policy therefore
prevents this branch from editing the issue or index directly. Once this branch
is accepted, the coordinator should mark all seven Ticket 02 items complete,
set Ticket 02 status to `complete`, and check Ticket 02 in the README, citing
this record. No later host/image certification item is claimed complete here.
