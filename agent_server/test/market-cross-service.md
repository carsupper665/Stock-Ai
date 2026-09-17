# Tickets 19–20 cross-service validation

## Public path under test

The tests exercise the public Agent runtime and Backend HTTP APIs across this path:

```text
Gateway fixture Tool Call
  -> AgentRuntime get_ohlcv / get_market_info
  -> Backend Account API token lookup
  -> Backend authenticated market router
  -> Backend market Runtime
  -> actual Binance adapter
  -> local Binance-contract source fixture
  -> actual Backend JSON
  -> correlated Tool Result
  -> next Gateway fixture turn
```

The Agent does not contact the source fixture directly. Accounts are created through
`POST /v1/accounts` with the configured Backend USER credential.

## Test-only Backend host

`backend/test/market_cross_service_host` composes the real Backend config loader,
database, auth/account services, router, market Runtime, and Binance source. The test
builds this package into `t.TempDir()` and supplies the local source origin through
`MARKET_CROSS_SERVICE_SOURCE_URL`; no production constructor or test hook is exposed.

The host binds `127.0.0.1:0`, announces its selected address, and is considered ready
only after a bounded `/v1/health` probe succeeds. Closing its stdin initiates bounded
HTTP shutdown. The test kills a host only if graceful shutdown exceeds its deadline.
The host binary, Backend SQLite database/logs, and Agent SQLite databases all live in
`t.TempDir()`. Source and Gateway fixtures use ephemeral listeners and are closed by
test cleanup.

## Assertions

- `get_ohlcv`: URI-encoded symbols cannot inject query parameters; source range and
  limit mapping are exact; out-of-range and incomplete candles are excluded; output
  is ascending, capped, and normalized to UTC before the next Gateway turn.
- Cancellation: the Agent Tool deadline reaches the request context observed by the
  local source, and the correlated result is `TOOL_TIMEOUT` with unknown outcome.
- `get_market_info`: Binance-required symbol/status/assets/order types/trading booleans
  are returned; decimal strings are unchanged; absent notional remains JSON `null`;
  `BREAK` remains a source fact while Backend execution remains virtual and does not
  claim to enforce source rules.
- Auth: an invalid configured Backend USER credential produces
  `BACKEND_AUTH_FAILED` during the real Account API lookup and performs no source I/O.

## Reproduction

Prerequisite: the Go toolchain versions declared by `agent_server/go.mod` and
`backend/go.mod`. No external service, fixed port, market credential, or persistent
database is required.

From `agent_server/`:

```powershell
go test ./test -run '^TestTicket19CrossService' -count=1
go test ./test -run '^TestTicket20CrossService' -count=1
go test ./test -run '^$' -count=1
```

Compile the test-only Backend host directly from `backend/`:

```powershell
go test ./test/market_cross_service_host -run '^$' -count=1
```

The TDD red run was the Ticket 19 command before the host package existed; it failed
while building `backend/test/market_cross_service_host`. Adding only that test host
made the same acceptance path green.
