# Ticket 05 evidence — Console authentication boundary

Date: 2026-10-11

## Controls

| Acceptance item | Evidence |
|---|---|
| Independently verify USER identity | `backend/internal/console/console.go` constructs the shared hardened OIDC verifier and requires the verified subject to match the single Gateway subject assertion. Console startup requires the complete HTTPS OIDC configuration and no longer loads `USER_TOKEN`. |
| Strip and recreate trusted headers | Every proxy path removes browser/Gateway authentication, cookie, and forwarding headers before setting exactly one selected upstream Authorization value. `TestConsoleRecreatesOnlyCorrectUpstreamCredential` exercises spoofed header removal on the wire. |
| Block unauthorized private access | `TestConsoleVerifiesOIDCBeforePrivateCredentialInjection` covers anonymous requests, spoofed assertions, wrong audience, wrong subject, assertion mismatch, Account-token attempts, unknown Agent/LLM paths, and wrong methods. Its shared upstream probe proves every 401/403/404/405 denial remains local. |
| Correct credential translation | The Console injects Agent Admin for Agent; LLM Runtime for model listing and generation; and LLM Admin for provider/model administration. The route table is asserted on captured upstream requests. |
| Preserve Account delegation | `TestConsoleKeepsOIDCUserAndAccountDelegationSeparate` proves an explicit opaque `/v1` Bearer remains an Account request even alongside a valid Gateway session, while session-only `/v1` uses the independently verified access token. The semantics are documented in `console-auth-boundary.md`. |
| Prevent disclosure | Internal credentials remain server-only. Private upstream auth errors become generic 502 responses. Browser encoding negotiation is removed; gzip is decoded to identity while unsupported encodings are rejected. A bounded response inspection rejects any internal credential in headers, redirects, decompressed bodies, or trailers, then removes all trailers and boundary headers. The disclosure tests check all three private credentials, gzip, unsupported encoding, and trailers. |

## Reproduction

Run from `backend/`:

```text
go test -race ./internal/console ./cmd/console ./test -run TestConsole -count=1
go test -race ./...
go vet ./...
```

Run from the repository root:

```text
python3 scripts/validate_security_contract.py
```

The first command covers the Ticket 05 threat matrix directly. The complete
Backend suite protects existing OIDC, Account, API, database, and process
behavior. The contract validator confirms that the executable authorization
matrix and closed R-001/R-003 evidence remain consistent.

## Recorded result

On 2026-10-11 the focused race suite and the complete Backend race suite passed,
followed by `go vet ./...`. The security-contract validator reported:

```text
security contract valid: 31 authorization cases, 14 risks
```

All 54 frontend Vitest tests passed and a fresh production bundle completed. A
post-build scan found no Agent Admin, LLM Admin, or LLM Runtime credential value
or server-side secret variable name. The Ticket 05 frontend change removes
Authorization from every `/agent` and `/llm` fetch so those requests use only the
HttpOnly Gateway session at the browser boundary.
