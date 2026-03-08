# Server Sandbox Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a deterministic spot-market sandbox in `server/` with replay control, order execution, and server-enforced data visibility boundaries.

**Architecture:** The server owns replay state, account state, order matching, and run-stage visibility. It exposes only the market/account/order information allowed for the current run stage and remains reproducible under fixed scenarios and config versions.

**Tech Stack:** Go 1.24, Gin, GORM, SQLite/Postgres, deterministic replay fixtures.

---

## Coupling and Readability Guardrails

- `controller/` only translates HTTP requests and responses. It must not contain trading rules or direct persistence logic.
- `service/` owns orchestration and depends on interfaces, not Gin context types.
- `sandbox/` owns replay, clock, and fill logic. It must not import `gin` or `gorm`.
- `model/store/` owns persistence models only. It must not contain matching or accounting rules.
- One file, one dominant responsibility:
  - `clock.go` only time / cursor movement
  - `engine.go` only replay state coordination
  - `matcher.go` only fill decisions
  - `account_service.go` only wallet and ledger mutations
  - `order_service.go` only order lifecycle orchestration
- replay advancement and fill application must be persisted atomically per step
- if one advance step mutates persisted state, it must commit or roll back as a unit:
  - clock advance
  - order eligibility check
  - fill writes
  - wallet updates
  - ledger writes
  - order status updates

These are mandatory review checkpoints, not style preferences.

### Task 1: Finish Bootstrapping and Route Wiring

**Files:**

- Modify: `server/main.go`
- Modify: `server/router/init.go`
- Modify: `server/router/api.go`
- Modify: `server/utils/init.go`
- Test: `server/test/`

**Objective:**

Make the server start cleanly and expose stable health/time routes.

**Steps:**

1. Wire `router.SetRouter(server)` and `router.ApiRouter(server)` in `main.go`.
2. Keep `server/.env` and `server/.env.example` aligned with runtime expectations.
3. Ensure `/api/v1/healthz` and `/api/v1/time` are live.
4. Add or update a small boot smoke test.

**Checkpoint:**

- `cd server && go test ./...`
- `cd server && go run .`
- `GET /api/v1/healthz` returns `200`

### Task 2: Extract Feed Abstraction and Replay Fixtures

**Files:**

- Create: `server/sandbox/feed.go`
- Create: `server/sandbox/replay_feed.go`
- Create: `server/sandbox/live_binance_feed.go`
- Modify: `server/controller/crypto.go`
- Test: `server/test/feed_test.go`

**Objective:**

Separate the trusted replay path from the existing live Binance helpers.

**Steps:**

1. Define a feed interface for loading scenarios, exposing current bar state, and advancing a cursor.
2. Wrap existing Binance access behind a live-feed adapter.
3. Implement replay fixtures that load deterministic OHLCV data.
4. Add tests for scenario loading, cursor movement, and symbol validation.

**Checkpoint:**

- replay feed tests pass
- existing price-cache tests still pass
- replay feed works without network access

### Task 3: Add Run Session and Visibility Guardrails

**Files:**

- Create: `server/sandbox/visibility.go`
- Create: `server/sandbox/run_session.go`
- Create: `server/service/run_service.go`
- Create: `server/model/store/run_store.go`
- Test: `server/test/data_visibility_test.go`

**Objective:**

Make future-data access impossible through the server API.

**Steps:**

1. Add a `RunSession` model with split ranges and version metadata.
2. Define visibility stages for training, execution, in-sample summary, OOS summary, and optional promotion.
3. Enforce prefix-only market reads during execution.
4. Add tests proving future bars and OOS summaries cannot be fetched early.

**Checkpoint:**

- early future-data requests fail
- OOS summaries remain locked until allowed
- run sessions persist version metadata

### Task 4: Add Market and Trading Persistence

**Files:**

- Create: `server/model/store/market_store.go`
- Create: `server/model/store/trading_store.go`
- Modify: `server/model/init.go`
- Test: `server/test/model_migration_test.go`

**Objective:**

Persist the minimum deterministic trading state required for spot evaluation.

**Steps:**

1. Add models for `MarketScenario`, `SymbolConfig`, `Bar`, `Wallet`, `LedgerEntry`, `Order`, and `Fill`.
2. Keep existing user/account bootstrap intact.
3. Update migrations to include the new models.
4. Test migration on fresh SQLite.

**Checkpoint:**

- fresh DB migrates cleanly
- existing tables still migrate
- `cd server && go test ./...`

### Task 5-1: Implement the Replay Engine and Clock

**Files:**

- Create: `server/sandbox/engine.go`
- Create: `server/sandbox/clock.go`
- Test: `server/test/sandbox_engine_test.go`

**Objective:**

Create deterministic replay advancement without mixing in wallet or order concerns.

**Steps:**

1. Implement replay advancement under a sandbox clock.
2. Keep engine state focused on scenario, cursor, and current visible market state.
3. Expose plain Go structs or interfaces for state reads so later services can depend on them without depending on HTTP or DB details.
4. Add tests for deterministic advancement, reset, and cursor invariants.

**Checkpoint:**

- replay advancement is deterministic
- reset returns the engine to a known baseline
- `engine.go` and `clock.go` compile without `gin` or `gorm` imports
- engine tests pass independently of account and order services

### Task 5-2: Implement Account, Order, and Matching Services

**Files:**

- Create: `server/sandbox/matcher.go`
- Create: `server/sandbox/slippage.go`
- Create: `server/service/account_service.go`
- Create: `server/service/order_service.go`
- Test: `server/test/order_flow_test.go`

**Objective:**

Add deterministic spot execution on top of the replay engine while keeping accounting, matching, and orchestration separated.

**Steps:**

1. Implement `account_service.go` for deposit, reset, wallet updates, and ledger writes.
2. Implement `order_service.go` for order validation, order creation, cancel, and query operations.
3. Keep `matcher.go` focused on pure fill decisions and price application rules.
4. Keep `slippage.go` focused on deterministic execution-cost logic.
5. Define a single transaction boundary for one persisted advance-and-fill step.
6. Add tests for insufficient balance, fills, cancel flow, fee accounting, ledger transitions, and rollback safety.

**Checkpoint:**

- wallet and ledger transitions are deterministic
- order lifecycle tests pass
- `matcher.go` has no HTTP or DB concerns
- `order_service.go` does not contain replay clock logic
- service and sandbox packages do not create circular dependencies
- a failed advance-and-fill step leaves no partial clock, fill, wallet, ledger, or order-status state behind

### Task 6: Expose Sandbox, Market, Account, and Spot APIs

**Files:**

- Create: `server/controller/market.go`
- Create: `server/controller/account.go`
- Create: `server/controller/spot.go`
- Create: `server/controller/sandbox.go`
- Modify: `server/router/api.go`
- Test: `server/test/api_test.go`

**Objective:**

Turn the route skeleton into a usable sandbox API.

**Steps:**

1. Implement handlers for market, account, spot, and sandbox routes.
2. Ensure run-stage visibility checks are enforced inside handlers or services.
3. Return stable JSON payloads that include execution metadata where relevant.
4. Add API tests for valid and invalid requests.

**Checkpoint:**

- all minimum phase 1 routes work
- invalid requests return clear `4xx`
- no route leaks future data

### Task 7: Server Regression and Integration Proof

**Files:**

- Create: `server/test/e2e_replay_run_test.go`
- Modify: `server/test/price_test.go` only if required and reviewed

**Objective:**

Prove the server is ready for the `brain` integration.

**Steps:**

1. Add one end-to-end replay test covering scenario load, deposit, order submission, run advance, and fill retrieval.
2. Verify the run session response includes:
   - `run_id`
   - `scenario_id`
   - `dataset_hash`
   - `execution_model_version`
   - `fee_model_version`
   - `slippage_model_version`
3. Run the full Go test suite.

**Checkpoint:**

- `cd server && go test ./...`
- replay integration test passes
- server is ready to serve deterministic runs to `brain`
