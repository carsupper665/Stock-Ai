# Server Live Price Buffer Design

## Purpose

`server/` needs one controlled live-price access path for reference market data. The goal is to make every internal live-price read behave like an in-memory read while keeping all external provider access behind one queue-driven subsystem.

This subsystem is not the sandbox pricing core. Sandbox execution continues to use replay data. This subsystem exists for live reference prices, diagnostics, and future `/market/live-price` style APIs.

## Scope

Phase 1 scope:

- one in-memory buffer pool for live symbol prices
- one deduplicated query queue
- one controlled background fetch worker
- one GC worker for idle symbols
- one internal status API for observing queue and symbol state
- reuse the existing logger implementation
- strict rule: controllers and services do not call provider APIs directly

Out of scope for phase 1:

- replacing replay pricing in sandbox execution
- multi-provider smart routing
- distributed cache
- custom logging infrastructure
- websocket-first optimization unless explicitly added later

## Core Invariants

- every live-price read must go through one manager entrypoint
- external provider calls must only happen inside the background fetch worker
- a symbol may appear in the queue at most once at a time
- cache hit must never trigger uncontrolled direct provider access
- rate-limit handling must be explicit, stateful, and observable
- stale data may be served, but it must be labeled as stale in memory state
- idle symbols must be removable without breaking future re-queue behavior

## Responsibilities

### Buffer pool

- store the latest known price state per symbol
- track fetch status, last access time, and last fetch result
- return cached data immediately when available

### Query queue

- accept new or refresh requests for symbols
- deduplicate repeated requests for the same symbol
- act as the only handoff point between reads and provider access

### Fetch worker

- pull symbols from the queue at a controlled pace
- call the provider fetcher
- update the buffer pool state
- apply backoff when the provider rate limits or errors

### GC worker

- scan buffered symbols on a fixed interval
- evict symbols that have been idle longer than the configured TTL
- preserve symbols that are actively fetching or still queued

### Status API

- expose queue length and worker health
- expose per-symbol state for debugging and audit
- expose failure and backoff information

## Data Model

### Price entry

Each symbol entry should contain at least:

- `symbol`
- `status`
- `price`
- `bid`
- `ask`
- `last_fetched_at`
- `last_access_at`
- `next_refresh_at`
- `last_error`
- `fail_count`
- `rate_limited_until`
- `source`

Suggested statuses:

- `cold`
- `queued`
- `fetching`
- `ready`
- `stale`
- `backoff`
- `error`

### Manager stats

The manager should also expose aggregate runtime stats:

- total buffered symbols
- queue length
- ready count
- stale count
- backoff count
- error count
- fetching count
- total fetch success count
- total fetch failure count
- total rate-limit count
- total GC eviction count

## Read Path

The read path should stay simple and non-blocking.

### `Get(symbol)` behavior

1. normalize the symbol
2. look up the entry in the buffer pool
3. if the symbol is unknown:
   - create a `cold` entry
   - update `last_access_at`
   - enqueue the symbol
   - return `pending / no data`
4. if the entry exists and is `ready`:
   - update `last_access_at`
   - if refresh is due, enqueue in background
   - return cached data immediately
5. if the entry exists and is `stale`, `backoff`, or `error` but still has data:
   - update `last_access_at`
   - if allowed, enqueue refresh
   - return cached data with current status
6. if the entry exists but has no usable data:
   - keep the current status
   - ensure it is queued if allowed
   - return `pending / no data`

Phase 1 should not block the request waiting on the provider.

## Query and Refresh Flow

### Queue rules

- queue input must be deduplicated by symbol
- one symbol must not have more than one pending queue slot at the same time
- enqueue attempts should be skipped when the symbol is already `queued` or `fetching`

### Worker rules

The fetch worker should:

1. pop one symbol from the queue
2. load the current entry
3. skip or delay if `rate_limited_until` is still active
4. mark the entry `fetching`
5. call the provider fetcher
6. on success:
   - update price fields
   - set `status=ready`
   - clear `last_error`
   - reset `fail_count`
   - schedule `next_refresh_at`
7. on provider error with existing cached data:
   - set `status=stale`
   - store `last_error`
   - increment `fail_count`
8. on provider error without cached data:
   - set `status=error`
   - store `last_error`
   - increment `fail_count`
9. on explicit provider rate limit:
   - set `status=backoff`
   - store `rate_limited_until`
   - store `last_error`

## Rate-Limit Controls

The design must assume provider limits are strict.

Phase 1 controls:

- one fetch worker by default
- fixed worker pacing interval
- one deduplicated queue
- stateful `backoff` per symbol
- provider `Retry-After` support when available
- no direct controller-to-provider calls

Optional future controls:

- global token bucket
- multiple worker lanes with shared limiter
- websocket-first provider with REST fallback

## GC Strategy

The GC worker should evict idle symbols when:

- `now - last_access_at > max_idle_ttl`
- the symbol is not currently `fetching`
- the symbol is not still pending in the queue

Eviction must only remove in-memory state. A later `Get(symbol)` must recreate the symbol entry and re-enqueue it normally.

## Logging and Observability

This subsystem must reuse the existing logger. Do not build a new logging framework.

Required log events:

- symbol first seen
- symbol enqueued
- fetch started
- fetch succeeded
- fetch failed
- provider rate-limited
- backoff applied
- stale data served
- GC eviction
- queue skip due to duplicate or full queue

The status API is the primary structured observability surface. Logs are for audit and diagnosis, not state transport.

## API Surface

Minimum internal status endpoints:

- `GET /api/v1/internal/live-price/status`
- `GET /api/v1/internal/live-price/symbol/:symbol`

Suggested status payload fields:

- queue length
- active worker count
- total symbol count
- counts per status
- fetch success / failure totals
- rate-limit total
- GC eviction total

Suggested symbol payload fields:

- symbol
- status
- latest price data
- last fetched time
- last accessed time
- next refresh time
- last error
- fail count
- rate-limited-until

## Proposed Code Layout

```text
server/
  liveprice/
    types.go
    manager.go
    fetcher.go
    worker.go
    gc.go
    status.go
    binance_rest_fetcher.go
  service/
    market_price_service.go
  controller/
    market.go
  test/
    live_price_manager_test.go
    live_price_status_test.go
```

## Testing Strategy

### Unit tests

- symbol normalization
- entry state transitions
- queue deduplication
- stale return behavior
- backoff behavior
- GC eviction rules

### Integration tests

- cache miss -> queued -> fetched -> ready
- ready hit -> no direct provider call from request path
- stale hit -> returns cached value and re-enqueues refresh
- rate-limit response -> backoff state visible in status API

### Regression requirement

- `go test ./...` from `server/` must remain green
- `go run main.go` must still start successfully

## Design Decisions Deferred

- whether phase 1 provider should be REST-only or REST-plus-WS
- whether the queue should be pure FIFO or refresh-priority aware
- whether stale data should include explicit max-staleness rejection rules
