# Ticket 03 completion evidence

Recorded: 2026-10-11

## Implemented controls

- Backend verifies RS256 JWT signature, exact algorithm/issuer/configured
  subject/token type, audience membership, expiration, and not-before time.
- JWKS retrieval is timeout-, size-, key-count-, and TTL-bounded. Cache-miss and
  same-`kid` signing-key rotation both refresh safely and fail closed. A shared
  refresh generation plus cooldown deduplicates concurrent misses and bounds
  retries for bogus key IDs, invalid signatures, and unavailable JWKS.
- `GET /v1/me` returns the authenticated OIDC USER or Account identity without
  returning credentials.
- JWT-shaped failures cannot fall back to opaque Account or static USER token
  resolution. Account authorization remains a separate lookup path.
- Static USER tokens require explicit opt-in and Release rejects that opt-in.
- Authentication responses and errors are generic and omit tokens, claims,
  public-key data, and internal JWKS addresses.

## Reproducible verification

From `backend/`:

```text
GOCACHE=/tmp/stock-ai-go-cache go test ./...
go vet ./...
GOCACHE=/tmp/stock-ai-go-cache go test -race ./test -run 'TestBackendOIDC(RefreshAmplificationIsBounded|ParallelRotationWaitersReuseRefresh)'
python3 test/run_all.py
```

Results on 2026-10-11: all Backend Go packages passed, `go vet` passed, and all
six Backend process-level test scripts passed. The three affected Agent-to-
Backend process integration tests also passed. The deployment security-contract
validator reported 31 valid authorization cases and its 16 unit tests passed.
The focused concurrent-refresh tests also passed under Go's race detector.

`test/backend_oidc_test.go` covers valid JWTs; wrong issuer, audience, subject,
algorithm, signature, and token type; expired and future-not-before tokens;
unavailable and oversized JWKS; cache reuse; same-key-ID rotation; Account
separation; no-fallback behavior; static-token opt-in; error disclosure; exact
refresh counts for repeated/concurrent misses; cooldown expiry; and unavailable
JWKS retry bounds.
`internal/api/middleware_test.go` covers authenticated USER and Account `/v1/me`
responses and anonymous rejection. `internal/config/config_test.go` covers the
Release static-token prohibition.
