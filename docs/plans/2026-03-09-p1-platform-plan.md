# P1 Platform Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add the P1 platform capabilities on top of the current P0 trading sandbox backend so admin users can manage replay datasets, control replay execution, manage live accounts, and monitor live symbol cache state without manual DB intervention.

**Architecture:** Extend the current `server/service` application layer rather than introducing a new package tree. Keep the existing `router -> controller -> service -> repo/store` flow, add new service modules for dataset import and live market state, and preserve the existing `MarketDataProvider` abstraction so replay and live providers remain swappable. P1 should treat live trading as metadata and market-data context only; order execution remains sandbox/replay-only in this phase.

**Tech Stack:** Go 1.24, Gin, GORM, SQLite/PostgreSQL, goroutines, WebSocket, CSV ingestion, existing `server/service` and `server/model/repo` abstractions.

---

## Current Baseline

The current P0 worktree already provides:
- Admin session login via `POST /admin/login`
- Sandbox CRUD plus `start/pause/stop`
- Virtual account CRUD within sandboxes
- Token create/rotate/revoke plus agent bearer auth
- Replay-backed market price/klines queries
- Market/limit/stop order flow for replay sandboxes
- Snapshot monitoring and `WS /ws/account`, `WS /ws/admin/monitor`

The concrete gaps that P1 must close are:
- Replay datasets must still be seeded directly in DB; there is no admin-facing dataset import flow
- Replay control has basic time mutation but no dedicated seek/speed/resume contract and no admin-facing operational semantics
- Live market data cache/update queue/dedup/GC is not implemented
- Live accounts do not yet have a product-complete CRUD and monitoring surface
- Monitor APIs do not expose live symbol subscription/cache health

### Task 1: Replay Dataset Management

**Files:**
- Create: `server/service/dataset.go`
- Create: `server/service/dataset_test.go`
- Modify: `server/model/store/user_store.go`
- Modify: `server/model/repo/repository.go`
- Modify: `server/controller/http.go`
- Modify: `server/router/api.go`
- Test: `server/router/http_integration_test.go`

**Step 1: Write the failing service tests**

```go
func TestDatasetServiceImportCSVCreatesDatasetAndRows(t *testing.T) {
    app := newTestApp(t)

    csv := "timestamp,open,high,low,close,volume\n2025-01-01T00:00:00Z,100,101,99,100,10\n"
    dataset, job, err := app.Datasets.ImportCSV(context.Background(), ImportDatasetInput{
        Name:   "btc-1m",
        Symbol: "BTCUSDT",
        CSV:    []byte(csv),
    })
    if err != nil {
        t.Fatal(err)
    }
    if dataset.ID == "" || job.Status != "completed" {
        t.Fatalf("unexpected dataset import result: %+v %+v", dataset, job)
    }
}

func TestDatasetServiceRejectsBrokenCSV(t *testing.T) {
    app := newTestApp(t)

    _, job, err := app.Datasets.ImportCSV(context.Background(), ImportDatasetInput{
        Name:   "broken",
        Symbol: "BTCUSDT",
        CSV:    []byte("bad,data"),
    })
    if err == nil || job.Status != "failed" {
        t.Fatalf("expected failed import job, got job=%+v err=%v", job, err)
    }
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./service -run TestDatasetService -v`
Expected: FAIL because `Datasets` service, import job model, and CSV ingestion logic do not exist yet.

**Step 3: Write minimal implementation**

```go
type DatasetImportJob struct {
    ID            string
    DatasetID     string
    Status        string
    ErrorSummary  string
    RowsTotal     int
    RowsImported  int
    StartedAt     *time.Time
    FinishedAt    *time.Time
}

type ImportDatasetInput struct {
    Name     string
    Symbol   string
    Interval string
    CSV      []byte
}
```

Implementation details to lock in:
- Add `dataset_import_jobs` persistence model with `pending/running/completed/failed` states
- CSV schema is fixed to `timestamp,open,high,low,close,volume`
- Validation happens synchronously before row insert
- Initial implementation may import within request transaction; job state still exists so UI can show audit/result history
- Add admin endpoints:
  `GET /admin/replay-datasets`
  `GET /admin/replay-datasets/:id`
  `POST /admin/replay-datasets`
  `POST /admin/replay-datasets/:id/import`

**Step 4: Run tests to verify they pass**

Run: `go test ./service -run TestDatasetService -v`
Expected: PASS

Then run: `go test ./router -run Dataset -v`
Expected: PASS after HTTP handlers are wired.

**Step 5: Commit**

```bash
git add server/model/store/user_store.go server/model/repo/repository.go server/service/dataset.go server/service/dataset_test.go server/controller/http.go server/router/api.go server/router/http_integration_test.go
git commit -m "feat: add replay dataset management"
```

### Task 2: Replay Control API and Sandbox Runtime Semantics

**Files:**
- Modify: `server/service/sandbox.go`
- Modify: `server/service/trading.go`
- Modify: `server/controller/http.go`
- Modify: `server/router/api.go`
- Test: `server/service/app_test.go`
- Test: `server/router/http_integration_test.go`

**Step 1: Write the failing tests**

```go
func TestSandboxReplayControlSeekRepricesMarketAndProcessesPendingOrders(t *testing.T) {
    app := newTestApp(t)
    seedReplaySandbox(t, app.DB, "sandbox-replay", "account-replay")

    ctx := context.Background()
    if _, err := app.Sandboxes.Start(ctx, "sandbox-replay"); err != nil {
        t.Fatal(err)
    }
    order, err := app.Trading.PlaceOrder(ctx, "account-replay", PlaceOrderInput{
        Symbol:       "BTCUSDT",
        Side:         store.OrderSideBuy,
        PositionSide: store.PositionSideLong,
        OrderType:    store.OrderTypeLimit,
        Quantity:     1,
        Price:        99,
        Leverage:     2,
    })
    if err != nil {
        t.Fatal(err)
    }

    _, err = app.Sandboxes.Seek(ctx, "sandbox-replay", time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC))
    if err != nil {
        t.Fatal(err)
    }
    order, err = app.Trading.GetOrder(ctx, order.ID)
    if err != nil || order.Status != store.OrderStatusFilled {
        t.Fatalf("expected seek to process pending limit order, got %+v err=%v", order, err)
    }
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./service -run ReplayControl -v`
Expected: FAIL because explicit replay control API and semantics do not exist.

**Step 3: Write minimal implementation**

```go
type ReplayControlStatus struct {
    CurrentTime time.Time
    Speed       float64
    Status      string
}
```

Implementation decisions:
- Add dedicated service methods: `Seek`, `SetReplaySpeed`, `Resume`
- Keep `Pause` and `Start` semantics distinct: `Start` for first activation, `Resume` for continuing a paused sandbox
- Every control action that changes effective replay time must call `Trading.ProcessSandbox`
- Add routes:
  `POST /admin/sandboxes/:id/replay/seek`
  `POST /admin/sandboxes/:id/replay/speed`
  `POST /admin/sandboxes/:id/replay/resume`

**Step 4: Run tests to verify they pass**

Run: `go test ./service -run ReplayControl -v`
Expected: PASS

Then run: `go test ./router -run Replay -v`
Expected: PASS

**Step 5: Commit**

```bash
git add server/service/sandbox.go server/service/trading.go server/controller/http.go server/router/api.go server/service/app_test.go server/router/http_integration_test.go
git commit -m "feat: add replay control endpoints"
```

### Task 3: Live Price Cache, Update Queue, Dedup, and GC

**Files:**
- Create: `server/service/live_market.go`
- Create: `server/service/live_market_test.go`
- Modify: `server/service/app.go`
- Modify: `server/service/infra.go`
- Modify: `server/domain/contracts.go`
- Modify: `server/controller/http.go`
- Modify: `server/router/api.go`
- Test: `server/router/http_integration_test.go`

**Step 1: Write the failing tests**

```go
func TestLiveMarketProviderDeduplicatesSubscriptions(t *testing.T) {
    provider := NewLiveMarketProvider(fakeFetcher{})

    provider.TouchSymbol("BTCUSDT")
    provider.TouchSymbol("BTCUSDT")

    snapshot := provider.DebugSnapshot()
    if snapshot["BTCUSDT"].SubscriberCount != 1 {
        t.Fatalf("expected deduplicated symbol subscription, got %+v", snapshot)
    }
}

func TestLiveMarketProviderGCRemovesIdleSymbols(t *testing.T) {
    provider := NewLiveMarketProvider(fakeFetcher{})
    provider.TouchSymbol("BTCUSDT")
    provider.ForceIdle("BTCUSDT", time.Hour)
    provider.RunGC(time.Now())

    if _, ok := provider.DebugSnapshot()["BTCUSDT"]; ok {
        t.Fatal("expected idle symbol to be removed")
    }
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./service -run LiveMarket -v`
Expected: FAIL because live market cache and symbol state machine do not exist.

**Step 3: Write minimal implementation**

```go
type SymbolState string

const (
    SymbolStateIdle     SymbolState = "idle"
    SymbolStateQueued   SymbolState = "queued"
    SymbolStateActive   SymbolState = "active"
    SymbolStateStale    SymbolState = "stale"
    SymbolStateRemoving SymbolState = "removing"
)

type LivePriceSnapshot struct {
    Symbol          string
    Price           float64
    LastUpdatedAt   time.Time
    LastAccessedAt  time.Time
    State           SymbolState
    SubscriberCount int
}
```

Implementation boundaries:
- In-memory first; no Redis in P1
- `LiveMarketDataProvider` shares the existing `MarketDataProvider` interface
- Add `TouchSymbol`, `FetchOrQueue`, `RunGC`, `DebugSnapshot`
- Background updater can initially poll via injected fetcher; no exchange order execution logic
- Add monitor endpoint: `GET /admin/monitor/live-symbols`

**Step 4: Run tests to verify they pass**

Run: `go test ./service -run LiveMarket -v`
Expected: PASS

Then run: `go test ./router -run LiveSymbols -v`
Expected: PASS

**Step 5: Commit**

```bash
git add server/service/live_market.go server/service/live_market_test.go server/service/app.go server/service/infra.go server/domain/contracts.go server/controller/http.go server/router/api.go server/router/http_integration_test.go
git commit -m "feat: add live market cache and symbol monitoring"
```

### Task 4: Live Account CRUD and Token Issuance

**Files:**
- Modify: `server/model/store/user_store.go`
- Modify: `server/model/repo/repository.go`
- Modify: `server/service/account.go`
- Modify: `server/service/token.go`
- Modify: `server/controller/http.go`
- Modify: `server/router/api.go`
- Test: `server/service/app_test.go`
- Test: `server/router/http_integration_test.go`

**Step 1: Write the failing tests**

```go
func TestLiveAccountCRUDAndTokenIssuance(t *testing.T) {
    app := newTestApp(t)
    ctx := context.Background()

    live, err := app.Accounts.Create(ctx, CreateAccountInput{
        Name:           "binance-paper-1",
        Type:           store.AccountTypeLive,
        InitialBalance: 5000,
    })
    if err != nil {
        t.Fatal(err)
    }
    if live.Type != store.AccountTypeLive || live.SandboxID != nil {
        t.Fatalf("unexpected live account: %+v", live)
    }

    token, _, err := app.Tokens.Create(ctx, CreateTokenInput{
        AccountID: live.ID,
        Name:      "live-token",
        Scopes:    []string{"account:read", "market:read"},
    })
    if err != nil || token == "" {
        t.Fatalf("expected token for live account, got token=%q err=%v", token, err)
    }
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./service -run LiveAccount -v`
Expected: FAIL because live account metadata and routes are incomplete.

**Step 3: Write minimal implementation**

```go
type LiveAccountCredentialMeta struct {
    Provider          string `json:"provider"`
    Environment       string `json:"environment"`
    CredentialsStatus string `json:"credentials_status"`
    SupportedSymbols  string `json:"supported_symbols"`
}
```

Implementation decisions:
- Extend `Account` with optional live metadata fields instead of creating a second table in P1
- Live account CRUD routes:
  `POST /admin/live-accounts`
  `GET /admin/live-accounts`
  `GET /admin/live-accounts/:id`
  `PATCH /admin/live-accounts/:id`
  `DELETE /admin/live-accounts/:id`
- Token issuance/revocation reuses existing token flow
- Live accounts are disabled for order placement in P1; they are market-data and monitoring entities only

**Step 4: Run tests to verify they pass**

Run: `go test ./service -run LiveAccount -v`
Expected: PASS

Then run: `go test ./router -run LiveAccount -v`
Expected: PASS

**Step 5: Commit**

```bash
git add server/model/store/user_store.go server/model/repo/repository.go server/service/account.go server/service/token.go server/controller/http.go server/router/api.go server/service/app_test.go server/router/http_integration_test.go
git commit -m "feat: add live account management"
```

### Task 5: Monitor Surfaces and Realtime Status for P1

**Files:**
- Modify: `server/service/monitor.go`
- Modify: `server/service/live_market.go`
- Modify: `server/controller/http.go`
- Modify: `server/router/api.go`
- Test: `server/router/http_integration_test.go`

**Step 1: Write the failing tests**

```go
func TestMonitorSnapshotIncludesReplayControlAndLiveSymbolHealth(t *testing.T) {
    app := newTestApp(t)
    // seed sandbox + live symbol cache state

    snapshot, err := app.Monitor.Snapshot(context.Background(), "sandbox-monitor")
    if err != nil {
        t.Fatal(err)
    }
    if snapshot.Sandbox.ReplayCurrentTime.IsZero() {
        t.Fatal("expected replay time in snapshot")
    }

    symbols, err := app.Monitor.LiveSymbols(context.Background())
    if err != nil || len(symbols) == 0 {
        t.Fatalf("expected live symbol health, got %+v err=%v", symbols, err)
    }
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./router -run Monitor -v`
Expected: FAIL because monitor payloads do not yet include live symbol status surfaces.

**Step 3: Write minimal implementation**

```go
type LiveSymbolHealth struct {
    Symbol          string `json:"symbol"`
    State           string `json:"state"`
    SubscriberCount int    `json:"subscriber_count"`
    LastUpdatedAt   string `json:"last_updated_at"`
    LastAccessedAt  string `json:"last_accessed_at"`
}
```

Implementation details:
- Extend monitor service with `LiveSymbols()`
- Keep `SandboxSnapshot` focused on a single sandbox; do not overload it with global cache state
- Expose global live symbol health through a separate endpoint and optional realtime topic for the admin monitor websocket
- Add events such as `live.symbol.queued`, `live.symbol.active`, `live.symbol.removed`

**Step 4: Run tests to verify they pass**

Run: `go test ./router -run Monitor -v`
Expected: PASS

Then run: `go test ./...`
Expected: PASS for the full worktree.

**Step 5: Commit**

```bash
git add server/service/monitor.go server/service/live_market.go server/controller/http.go server/router/api.go server/router/http_integration_test.go
git commit -m "feat: add p1 monitor status surfaces"
```

## Acceptance Scenarios
- Admin uploads a valid CSV OHLCV file, sees import status `completed`, binds the dataset to a sandbox, and starts replay without touching the DB directly.
- Admin seeks replay time forward and pending limit/stop orders are re-evaluated immediately against the new replay time.
- A live symbol requested multiple times only creates one active cache entry and is removed after idle expiry.
- Admin creates a live account, sees credentials status and supported symbols metadata, and can issue a token.
- Monitor endpoints expose both sandbox replay status and live symbol cache health without mixing per-sandbox and global concerns.

## Final Verification
Run from `D:\GO PROJ\Stock-Ai\.worktrees\trading-sandbox-mvp\server`:

```bash
go test ./service -run Dataset -v
go test ./service -run ReplayControl -v
go test ./service -run LiveMarket -v
go test ./service -run LiveAccount -v
go test ./router -run Dataset -v
go test ./router -run Replay -v
go test ./router -run LiveSymbols -v
go test ./router -run Monitor -v
go test ./...
```

Expected: every targeted suite passes, and `go test ./...` finishes with `ok` for `server/service` and `server/router`.
