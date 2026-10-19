# Production secret and configuration safety

**Control ID:** `SEC-SECRETS-02`  
**Applies to:** Windows Test and Release hosts and every production container  
**Gate:** `scripts/production-secrets.ps1 Validate` must pass before Compose creates a
container.

This control implements the secret rules in `SEC-DEPLOY-01`. Test and Release
are separate security domains. A namespace is generated on its destination
Windows host; it is never copied from the other host and never enters source,
the Docker build context, an image, Compose interpolation, browser assets, or a
command-line argument.

## Destination-side generation

Run PowerShell as the destination host's deployment administrator. Use an
absolute directory below a host-local protected deployment directory. The final
directory must not already exist; generation refuses replacement so an existing
LLM master key cannot be accidentally rotated.

```powershell
# Test computer only
.\scripts\production-secrets.ps1 Generate -Environment Test `
  -Directory 'D:\StockAI\Test\secrets\stock-ai-test' `
  -BackendDatabase 'D:\StockAI\Test\data\backend.db' `
  -ProviderDatabase 'D:\StockAI\Test\data\llm-provider.db'

# Release computer only
.\scripts\production-secrets.ps1 Generate -Environment Release `
  -Directory 'D:\StockAI\Release\secrets\stock-ai-release' `
  -BackendDatabase 'D:\StockAI\Release\data\backend.db' `
  -ProviderDatabase 'D:\StockAI\Release\data\llm-provider.db'
```

Generation uses the operating system CSPRNG, creates independent values for
each credential role, writes files without echoing their contents, removes ACL
inheritance on Windows, and grants full control only to the generating identity,
local Administrators, and SYSTEM. Do not capture or publish the directory. Do
not generate both environments on one computer in real deployments; the two
commands above show the environment-specific form, not a shared-host workflow.

The non-secret `production-config.json` records the environment, namespace,
secret sources, Provider database path, and exact container mount allowlist. It
contains no secret values. `legacy_user_auth` is fixed to `false`; static USER
browser login is forbidden. `backend_middleware_token` is the Backend's USER
credential, held only by Backend and the middleware. `backend_user_token` is a
separate private service credential used only by Backend and Agent Server (for
Account lookup); neither is a browser login.

## Mandatory pre-start validation and safe diagnostics

On each host, validate its own namespace before startup. During a controlled
configuration review, compare the two namespaces from an administrator machine
that is explicitly authorized to access both; routine Test operation must not
receive the Release directory.

```powershell
.\scripts\production-secrets.ps1 Validate -Environment Release `
  -Directory 'D:\StockAI\Release\secrets\stock-ai-release'
```

Validation exits nonzero for a missing value, a known development/placeholder
value, fewer than 32 bytes, low character diversity, a duplicate credential,
wrong namespace, wrong environment marker, writable/readable-by-others POSIX
mode, malformed key, legacy USER login, or a mount outside the allowlist. The
LLM and cookie keys must be canonical base64 encodings of exactly 32 random
bytes. Diagnostics contain only setting name, source path, validity, and a
reason category. They never contain a value, hash, prefix, suffix, or masked
fragment.

The Backend uses `backend_account_token_key` for AES-GCM encryption plus a
database-enforced unique keyed lookup of Account tokens at rest. The lookup is
nullable in development/no-key mode, where the existing plaintext uniqueness
constraint remains authoritative. Existing plaintext rows are encrypted during
the first migration with the key; a wrong key fails migration rather than
silently making credentials unusable. Preserve this key with every retained
Backend database, under the same recovery-set rule as the Provider database.

The validator must finish successfully **before** the LLM Provider process can
open its database. `LLM_SERVER_MASTER_KEY` is then read from the validated
`llm_master_key` file. The LLM service also rejects a key that cannot decrypt an
existing Provider token. If `llm-provider.db` is retained, restored, backed up,
or rolled back, its matching `llm_master_key` must be retained and protected as
one recovery set. Losing or replacing the key makes the encrypted Provider
tokens unrecoverable. Never generate a replacement over an existing namespace;
key rotation requires a separately reviewed decrypt-and-re-encrypt procedure.

## Container access contract

Compose must use Docker secrets or bind-mounted individual files, never mount
the namespace directory. Every mount is read-only and has exactly the consumers
below. The configuration validator rejects additions, omissions, writable
mounts, alternate targets, and a Test filename in Release (or vice versa).

| Secret | Allowed service containers |
|---|---|
| `backend_user_token` | Backend, Agent Server |
| `backend_middleware_token` | Backend, Middleware |
| `backend_account_token_key` | Backend |
| `agent_admin_token` | Agent Server, Middleware |
| `llm_admin_token` | LLM Provider Server, Middleware |
| `llm_runtime_token` | LLM Provider Server, Agent Server, Middleware |
| `oidc_client_secret` | Middleware |
| `oidc_cookie_key` | Middleware |
| `llm_master_key` | LLM Provider Server |

Nginx and frontend build containers receive none of these files. A secret is
mounted at `/run/secrets/<environment-prefixed-name>` in only the listed service
containers. Production images must accept the corresponding `_FILE` source and
must not receive a secret through `ARG`, `ENV`, labels, or layer contents. Ticket
08's Compose gate must materialize this recorded allowlist; Ticket 07's image
gate must scan every final image first.

## Non-disclosure audit

After building frontend assets and saving each image as a tar archive, capture
representative normal logs plus HTTP success and error responses without request
authorization headers. Scan all artifacts using the host-local namespace:

```powershell
.\scripts\production-secrets.ps1 Scan -Environment Test `
  -Directory 'D:\StockAI\Test\secrets\stock-ai-test' `
  -Artifact '.\evidence\backend-image.tar', '.\frontend\dist', `
            '.\evidence\http-responses', '.\evidence\normal-logs'
```

The scan performs an exact byte search locally and reports only artifact path
and pass/fail. It never reports which value matched. Any match blocks promotion;
rotate the disclosed credential in its own environment after removing it from
the artifact and its history.

## Automated evidence

Run from the repository root:

```text
python3 -m unittest scripts.test.test_production_secrets -v
python3 -m unittest scripts.test.test_security_contract -v
python3 scripts/validate_security_contract.py
```

`test_production_secrets` proves generation silence and independence, rejects
weak/missing/placeholder/duplicate and cross-environment values, validates the
master-key format and file permissions, asserts value-free diagnostics, and
injects a synthetic configured secret into each of an image archive, frontend
bundle, HTTP response, error response, and normal log to prove each disclosure
is detected. Real image/frontend/runtime evidence is collected again when those
artifacts exist under Tickets 07, 09, and 10.
