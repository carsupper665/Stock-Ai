# Server Sandbox Design

## Purpose

`server/` is the deterministic source of truth for sandbox market state, account state, order execution, and run-stage data visibility. It exists to make strategy evaluation reproducible and auditable.

## Scope

Phase 1 scope:

- spot market only
- deterministic replay feed as the primary execution mode
- optional live Binance adapter kept only for development reference
- wallet, ledger, orders, fills, and summary APIs
- split-aware run sessions that prevent future data leakage

Out of scope for phase 1:

- futures execution
- real-money trading
- exchange-grade microstructure simulation
- arbitrary strategy logic execution inside the server

## Core Invariants

- identical scenario + identical strategy orders + identical config must produce identical fills and balances
- the server must never expose future bars for a locked run stage
- execution model, fee model, and slippage model must be versioned
- run state must remain resettable and replayable
- replay advancement and fill application must be persisted atomically per step

## Responsibilities

### Market and replay

- load scenarios from fixtures or datasets
- expose current replay cursor and current visible market state
- advance replay deterministically under a sandbox clock

### Execution and accounting

- validate orders against symbol rules and wallet state
- fill market and limit orders under deterministic rules
- persist fills, fees, balances, and ledger entries
- treat one replay advance step as one transaction boundary whenever it mutates persisted state

### Run-stage data control

- create run sessions with declared data splits
- enforce which bars are visible at each stage
- expose summaries only when the stage allows them

## Market Modes

### Recommended primary mode: replay

Replay historical OHLCV bars under a server-controlled clock.

Why this is primary:

- reproducible
- good for walk-forward and OOS validation
- compatible with deterministic tests

### Secondary mode: synthetic

Synthetic regime generators are allowed for stress tests:

- trend
- chop
- low-liquidity spike
- gap / shock

### Reference-only mode: live Binance

The current Binance websocket and REST helpers may remain, but they are not part of the trusted evaluation path.

## Run Session and Data Firewall

The server must enforce a split-aware run session model. A run session should define:

- `run_id`
- `scenario_id`
- `dataset_hash`
- `train_range`
- `validation_range`
- `oos_range`
- optional `promotion_range`
- `execution_model_version`
- `fee_model_version`
- `slippage_model_version`

### Visibility rules

- Drafting stage:
  - server may expose only approved training metadata or approved training bars
- Replay execution stage:
  - server exposes only bars up to the current cursor
- In-sample summary stage:
  - server may expose in-sample metrics after the candidate is frozen
- OOS stage:
  - OOS metrics stay locked until the OOS run completes
- Promotion stage:
  - if enabled, this holdout remains hidden until all earlier gates pass

The firewall must be code-enforced. The client must not decide what is visible.

## Matching and Fill Model

Phase 1 stays intentionally conservative:

- market order:
  - fill on next bar open
  - apply fee and deterministic slippage
- limit buy:
  - eligible if bar low reaches the limit
  - fill at limit price
- limit sell:
  - eligible if bar high reaches the limit
  - fill at limit price

Phase 1 does not need partial fills, queue position, or full depth simulation.

## Data Model

### Core execution models

- `MarketScenario`
- `SymbolConfig`
- `Bar`
- `RunSession`
- `Wallet`
- `LedgerEntry`
- `Order`
- `Fill`

### Version / audit metadata

- `ExecutionModelVersion`
- `FeeModelVersion`
- `SlippageModelVersion`

These may be stored either as tables or stable version strings attached to the run session, but they must be persisted and queryable.

## API Surface

Minimum phase 1 routes:

- `GET /api/v1/healthz`
- `GET /api/v1/time`
- `GET /api/v1/market/price`
- `GET /api/v1/market/klines`
- `GET /api/v1/account/balances`
- `GET /api/v1/account/ledger`
- `GET /api/v1/account/summary`
- `POST /api/v1/account/deposit`
- `POST /api/v1/account/reset`
- `POST /api/v1/spot/order`
- `DELETE /api/v1/spot/order`
- `GET /api/v1/spot/openOrders`
- `GET /api/v1/spot/myTrades`
- `POST /api/v1/sandbox/loadScenario`
- `POST /api/v1/sandbox/runs`
- `POST /api/v1/sandbox/runs/:runId/advance`
- `GET /api/v1/sandbox/runs/:runId/state`

If future-sensitive endpoints are added later, they must respect run-stage visibility.

## Proposed Code Layout

```text
server/
  main.go
  router/
    api.go
    init.go
  controller/
    market.go
    account.go
    spot.go
    sandbox.go
  service/
    market_service.go
    account_service.go
    order_service.go
    run_service.go
  sandbox/
    engine.go
    clock.go
    feed.go
    replay_feed.go
    synthetic_feed.go
    live_binance_feed.go
    matcher.go
    visibility.go
  model/
    init.go
    store/
      user_store.go
      market_store.go
      trading_store.go
      run_store.go
  test/
    api_test.go
    feed_test.go
    sandbox_engine_test.go
    order_flow_test.go
    data_visibility_test.go
```

## Testing Strategy

### Unit tests

- replay feed loading
- cursor advancement
- fill rules
- wallet transitions
- fee and slippage application

### Integration tests

- deposit -> order -> fill -> ledger -> summary
- cancel flow
- reset flow

### Security / validity tests

- future bars cannot be requested before the run stage allows them
- OOS metrics cannot be queried early
- version metadata is always present on run state responses

### Regression requirement

`go test ./...` from `server/` must remain green, including the existing price cache tests unless deliberately superseded.

## Design Decisions Deferred

- whether stop orders enter phase 1 or phase 2
- whether positions are stored explicitly or derived from fills for spot summaries
- whether promotion-range holdout is mandatory in phase 1 or configurable
