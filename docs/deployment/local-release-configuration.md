# Isolated local Release configuration (Ticket 11)

The supported local Release consumes the four application digests and the
OIDC Gateway digest from the complete, checksummed T10 promotion package.
Preparation verifies every referenced promotion artifact before writing the
private env file. Release startup never builds images.

| Boundary | local-release value |
| --- | --- |
| Compose project | `stock-ai-local-release` |
| Environment and secrets | `Release`, `stock-ai-release`, ignored `.local-release/` |
| Public origin | `https://app.localrelease:8443` |
| Published ingress | Nginx only, `127.0.0.1:8443/tcp` |
| Issuer | `https://app.localrelease:8443/realms/stock-ai-release` |
| Fixture client | `stock-ai-release` with a generated independent secret |
| Redirect / logout | Public origin plus `/oauth2/callback` / `/` |
| API audience / allowed subject | `stock-ai-api-release` / `local-release-user` |
| Cookie | `__Host-stock-ai-release-session`, independent generated key |
| Networks | `stock-ai-local-release_application`, `stock-ai-local-release_edge` |
| SQLite volumes | `stock-ai-local-release_backend_state`, `stock-ai-local-release_agent_state`, `stock-ai-local-release_llm_state` |

All commands below run from the repository root. Docker Engine must already
contain the approved immutable images, or be able to pull them from the local
registry. A fresh machine needs the approved auxiliary digest in addition to
the T10 product package; missing images must be imported/pulled, never rebuilt
by the Release workflow.

```sh
python3 scripts/local-release/prepare.py
python3 scripts/local-release/lint.py --evidence deploy/evidence/local-release/t11/configuration-lint.json
python3 scripts/local-release/start.py
python3 scripts/local-release/verify_ingress.py --evidence deploy/evidence/local-release/t11/curl-ingress.json
python3 scripts/local-release/verify_runtime.py --test-project stock-ai-t10-cert --evidence deploy/evidence/local-release/t11/runtime-isolation.json
```

`start.py` calls the lint before any Compose startup operation and uses
`up -d --no-build --wait`. To select another certified promotion package, pass
the same `--manifest <path>/promotion.json` to prepare, lint and start.
The manifest SHA-256 is bound into the prepared env file, as is the auxiliary
approval file hash. Host environment variables cannot override env-file
interpolation. The Compose inputs are `deploy/compose/compose.yaml` and
`deploy/compose/compose.local-release.yaml`; the Test overlays are never used.
The runtime isolation verifier requires the actual Test project name; the
accepted T10 run uses `stock-ai-t10-cert`. It refuses to claim isolation if
the supplied Test project is absent.

Preparation creates secrets directly in `.local-release/secrets` with mode
0440, a mode-0600 env file, and separate self-signed TLS/JWKS private keys.
The runtime/secrets directories are mode 0700 and the certificate directory
is mode 0750. Services retain their unprivileged user IDs with the current
host GID, allowing read access to mounted material without world-readable
secrets. Rerunning preparation validates and preserves existing valid keys
and secrets; malformed, partial, expired or symlinked material blocks it.
Certificates last two days for local acceptance. Deliberate rotation requires
stopping the Release project and securely replacing its own material; the
helper never silently rotates it or edits Test files.

For browser access, resolve `app.localrelease` to `127.0.0.1` locally and trust
only the generated local certificate as appropriate. The automated curl
verification uses `--resolve`, `--cacert`, and `--noproxy '*'`, so it requires
no hosts-file change or public DNS/TLS. It does not disable TLS verification.
The unchanged Nginx login limit can return 429 during repeated checks. Curl
verification records each real HTTP status and retries 429/503 at 12-second
intervals, with at most five retries per request. Exhaustion writes failed
evidence and exits nonzero; headers, cookie values and query strings stay
private. Ordinary authentication failures are never converted into retries.
The fixture accepts only its configured redirect and logout URIs. Containers
resolve `app.localrelease` to the private IdP fixture, which listens on 8443
with the same certificate and issuer. Browser authorization/logout requests
reach it through Nginx on the same public origin. The fixture provider stays
private at `http://fake-provider.localrelease:18080/v1`; its independently
generated provider token is a private secret for later business acceptance.
Nginx exposes only the protocol routes, excluding fixture control routes.

The fixture is an auxiliary image, distinct from the promoted product images.
T11 parameterizes and tests its runtime identity configuration, with the old
Test defaults preserved. Its image was built once in the Test auxiliary-image
workflow and stored in the local registry by immutable digest.
`deploy/local-release/auxiliary-images.json` records that tested digest and
the unchanged T10-tested Nginx digest. Release uses them without a build
directive or source compilation. No product or Gateway image was rebuilt.

The lint rejects Test identifiers, placeholders, unresolved variables,
unknown services, mutable or substituted images, any build directive,
additional/non-loopback/wrong ports, shared/external networks or volumes,
missing writable SQLite/log/history persistence, incorrect identity or
cookie policy, missing private secret mounts and malformed/insecure material.
Loopback hosts and the local certificate are valid inputs.
`ALLOW_STATIC_USER_TOKEN=false` is enforced both in the Release overlay and
by lint; Release user authentication requires signed OIDC credentials.
The Agent Server's legacy private Backend calls still expect a static user
token; enabling it in Release is forbidden. T15 business acceptance must
handle that existing limitation through the supported account-scoped
credential path or a separately certified product change, not a fixture
authentication bypass.

Validation performed locally:

- 110 repository Python tests passed, plus four private-material tests and
  ten Release configuration/ingress tests, including 17 rejected config mutations,
  placeholder/symlink secrets, env override rejection, static-auth rejection,
  and startup refusing to proceed when lint fails.
  Retry tests cover bounded 429/503 waits, JSON curl output and non-retryable 401.
- Four Go fixture tests passed, including Release PKCE/token exchange and
  rejection of Test redirect, logout and client secret.
- The real stack started using immutable digests and all six checked services
  were healthy. Only Nginx published a host port.
- Real certificate-verified curl followed `/ → /oauth2/start →
  /realms/stock-ai-release/authorize → /oauth2/callback → /`.
  Every hop used `app.localrelease:8443`; `curl --location` completed with
  four redirects and HTTP 200. Cookie-authenticated API access returned 200;
  anonymous API access returned 401.
  An immediate repeat encountered HTTP 429, waited 12 seconds, then passed
  with HTTP 200; `curl-ingress-rerun.json` records the actual status sequence.

Sanitized evidence is under `deploy/evidence/local-release/t11/`; it contains
no cookies, tokens, authorization-code queries, secrets, certificate/private
key contents or user data. This completes the configuration/ingress scope;
T14 preflight/deployment and T15 business migration/acceptance remain separate.
