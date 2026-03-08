# Server Live Price Buffer Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build one queue-driven in-memory live-price buffer so internal callers read cached symbol prices through a single entrypoint instead of calling provider APIs directly.

**Architecture:** The manager owns the buffer pool, queue, fetch worker, GC worker, and status reporting. External provider access is only allowed inside worker-controlled fetch code.

**Tech Stack:** Go 1.24, in-memory map + queue, existing logger, current Binance provider helpers behind a fetcher interface.

---

## Coupling and Readability Guardrails

- `liveprice/` owns live-price buffering, queueing, and provider fetch orchestration.
- `controller/` only translates HTTP requests and responses. It must not call provider code directly.
- `service/market_price_service.go` may depend on `liveprice.Manager`, not on raw Binance helpers.
- provider access must be hidden behind a narrow fetcher interface.
- do not build a new logging subsystem; reuse the existing logger.
- one symbol must never be queued more than once at the same time.
- request-path reads must not synchronously call the provider in phase 1.

These are mandatory review checkpoints, not style preferences.

### Task 1: Define Live Price Types and Entry States

**Files:**

- Create: `server/liveprice/types.go`
- Create: `server/liveprice/status.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Create the shared types for symbol entries, manager stats, and status values.

**Steps:**

1. Define the `PriceEntry` shape and status constants.
2. Define aggregate manager stats and response-friendly snapshot types.
3. Keep all types independent from Gin and GORM.
4. Add small tests for symbol normalization and status defaults.

**Checkpoint:**

- live price types compile cleanly
- no Gin or GORM imports in `liveprice/types.go`
- tests for status defaults pass

### Task 2: Build the Buffer Pool and Deduplicated Queue

**Files:**

- Create: `server/liveprice/manager.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Make one manager own entry lookup, enqueue decisions, and symbol deduplication.

**Steps:**

1. Implement the in-memory symbol map.
2. Implement queue bookkeeping so repeated requests for the same symbol do not create duplicate queue entries.
3. Implement `Get(symbol)` to return cached data or `pending` state.
4. Update `last_access_at` on every read.

**Checkpoint:**

- repeated requests for one unknown symbol enqueue only once
- cache hit reads return immediately from memory
- request-path code does not call the provider directly

### Task 3: Add the Controlled Fetcher Interface and Worker

**Files:**

- Create: `server/liveprice/fetcher.go`
- Create: `server/liveprice/worker.go`
- Create: `server/liveprice/binance_rest_fetcher.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Move all provider access behind one worker-controlled fetch path.

**Steps:**

1. Define a narrow fetcher interface.
2. Wrap the existing Binance helper behind that interface.
3. Add one background worker with fixed pacing.
4. Update symbol entries on success and failure.

**Checkpoint:**

- external provider access happens only through the worker
- success updates entries to `ready`
- provider failures are visible in entry state
- request-path code still does not block on provider calls

### Task 4: Add Backoff and Rate-Limit Handling

**Files:**

- Modify: `server/liveprice/worker.go`
- Modify: `server/liveprice/status.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Make rate-limit behavior explicit, observable, and safe.

**Steps:**

1. Detect provider rate-limit errors and populate `rate_limited_until`.
2. Move affected symbols into `backoff` state.
3. Respect provider `Retry-After` when available.
4. Count rate-limit events in manager stats.

**Checkpoint:**

- simulated rate-limit responses move symbols into `backoff`
- worker does not hammer a symbol that is still in backoff
- status snapshots expose backoff timing and counts

### Task 5: Add Stale-Serve and Refresh Semantics

**Files:**

- Modify: `server/liveprice/manager.go`
- Modify: `server/liveprice/worker.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Allow cached data to remain usable while refresh happens in the background.

**Steps:**

1. Define when `ready` becomes refreshable.
2. Define when a failed refresh produces `stale` instead of `error`.
3. Return stale data immediately while re-enqueueing refresh when allowed.
4. Keep miss behavior non-blocking.

**Checkpoint:**

- stale entries can still be read
- stale reads do not synchronously call the provider
- refresh requests are re-enqueued in a controlled way

### Task 6: Add GC for Idle Symbols

**Files:**

- Create: `server/liveprice/gc.go`
- Modify: `server/liveprice/manager.go`
- Test: `server/test/live_price_manager_test.go`

**Objective:**

Prevent the in-memory symbol map from growing without bound.

**Steps:**

1. Add a GC worker with fixed scan interval.
2. Evict symbols whose `last_access_at` exceeds the configured idle TTL.
3. Do not evict symbols that are `fetching` or still queued.
4. Log every eviction through the existing logger.

**Checkpoint:**

- idle symbols are evicted on schedule
- active or queued symbols are preserved
- re-requesting an evicted symbol re-creates and re-queues it cleanly

### Task 7: Expose Internal Status APIs

**Files:**

- Create: `server/service/market_price_service.go`
- Modify: `server/controller/market.go` or create it if still absent
- Modify: `server/router/api.go`
- Test: `server/test/live_price_status_test.go`

**Objective:**

Make the subsystem observable without reading logs directly.

**Steps:**

1. Add a service wrapper over the manager.
2. Expose aggregate status and per-symbol status endpoints.
3. Return stable JSON payloads for queue and symbol state.
4. Ensure handlers still do not call provider code directly.

**Checkpoint:**

- internal status endpoint returns queue and status counts
- symbol endpoint returns detailed state for one symbol
- controllers depend on the service or manager snapshots, not on fetchers

### Task 8: Wire Future Live Price Reads Through the Manager

**Files:**

- Modify: future market controllers/services as needed
- Test: `server/test/live_price_status_test.go`

**Objective:**

Establish the manager as the only approved live-price entrypoint.

**Steps:**

1. Route any new live-price read path through the manager.
2. Reject direct provider usage in request-path code review.
3. Keep sandbox replay pricing unchanged.

**Checkpoint:**

- live-price reads go through the manager only
- sandbox pricing still uses replay data
- no controller or request-path service directly uses Binance helpers

### Task 9: Regression and Non-Rate-Limit Proof

**Files:**

- Test: `server/test/live_price_manager_test.go`
- Test: `server/test/live_price_status_test.go`
- Existing tests: `server/test/price_test.go` and current server suite

**Objective:**

Prove the subsystem is safe, testable, and does not regress existing behavior.

**Steps:**

1. Run the full Go test suite.
2. Verify startup still works.
3. Verify status endpoints and manager tests both pass.
4. Review logs and stats behavior during simulated rate-limit and stale scenarios.

**Checkpoint:**

- `cd server && go test ./...`
- `cd server && go run main.go`
- `python test/api_smoke.py`
- manager tests prove no direct request-path provider access
- rate-limit scenarios do not create uncontrolled retry loops

## Existing Third-Party API Touchpoints

The following locations currently call third-party APIs or external network providers and must be tracked during replacement work.

### Live Price Path and Direct Replacement Targets

- `server/sandbox/live_binance_feed.go`
  - Binance REST API: `https://api.binance.com/api/v3/ticker/bookTicker`
  - current role: direct live bid/ask lookup
  - replacement intent: future live-price reads must go through `liveprice.Manager` and worker-controlled fetchers instead of request-path direct calls
- `server/utils/price.go`
  - Binance WebSocket API: `wss://stream.binance.com:9443/ws/<symbol>@miniTicker`
  - current role: in-memory per-symbol websocket price cache
  - replacement intent: any reused websocket logic must be hidden behind the new live-price subsystem; request-path code must not depend on this package directly

### Other Existing Third-Party API Touchpoints

- `server/utils/utils.go`
  - Discord webhook HTTP POST in `SendErrorToDc`
  - generic external file download in `DownloadFile`
  - note: not part of live-price replacement scope, but must keep working if touched by related refactors
- `server/utils/updater.go`
  - GitHub Releases API: `https://api.github.com/repos/<repo>/releases/latest`
  - asset download through GitHub release URLs
  - note: not part of live-price replacement scope, but must keep working if touched by related refactors
- `brain/`
  - current scan result: no active third-party API call sites were found in the current tree

## Final Replacement and Regression Requirement

After all live-price tasks are complete, replacement work is not done until both conditions below are true.

1. Original live-price provider entrypoints have been replaced or wrapped so request-path code no longer depends on raw third-party API helpers directly.
2. Broad regression coverage proves the original modules still work and the new live-price path did not break unrelated behavior.

### Required End-State Rules

- all future live-price reads must flow through `liveprice.Manager` or its service wrapper
- request-path code must not directly call Binance REST or Binance WebSocket helpers
- sandbox replay pricing must remain unchanged and must not start depending on live provider data
- if old provider helpers are retained internally, they must sit behind the manager/fetcher boundary only
- unrelated third-party integrations such as Discord webhook and GitHub updater must remain functional unless explicitly removed by a separate plan

### Required Final Regression Coverage

Reuse existing tests where possible and add new integration tests where replacement changes behavior.

- keep `server/test/price_test.go` green or replace it with equivalent coverage if the old live price cache is retired
- keep sandbox regression tests green: `server/test/feed_test.go`, `server/test/data_visibility_test.go`, `server/test/model_migration_test.go`, `server/test/sandbox_engine_test.go`, `server/test/order_flow_test.go`
- keep API smoke coverage green through `test/api_smoke.py`
- add final live-price manager and status API coverage for queue deduplication, stale-serve, backoff, and no direct request-path provider access
- if old API logic is replaced, prove the new manager path serves the same required symbol data shape to callers

### Required Final Verification Commands

- `cd server && go test ./...`
- `cd server && go run main.go`
- `python test/api_smoke.py`

These checks are mandatory before the replacement work can be considered finished.
