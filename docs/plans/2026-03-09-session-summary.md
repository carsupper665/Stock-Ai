# 2026-03-09 Session Summary

## Scope of This Session

This session covered four parallel tracks:

1. bootstrap and stabilize the Go server runtime
2. split and refine the `server` / `brain` design and implementation plans
3. implement the early `server` sandbox tasks
4. pause the next sandbox task and define a separate live-price buffer architecture

## Documents Created or Restructured

The following planning documents were created or materially updated during this session:

- `docs/plans/2026-03-09-crypto-sandbox-market-design.md`
- `docs/plans/2026-03-09-crypto-sandbox-market-implementation-plan.md`
- `docs/plans/2026-03-09-server-sandbox-design.md`
- `docs/plans/2026-03-09-brain-strategy-research-design.md`
- `docs/plans/2026-03-09-server-sandbox-implementation-plan.md`
- `docs/plans/2026-03-09-brain-strategy-research-implementation-plan.md`
- `docs/plans/2026-03-09-server-live-price-buffer-design.md`
- `docs/plans/2026-03-09-server-live-price-buffer-implementation-plan.md`

## Key Design Decisions

### Brain Boundaries

The `brain` side was tightened so it does not drift into a narrative-only review assistant.

- strategy mutation must stay inside a constrained DSL / schema boundary
- legal hypothesis space must be explicit
- data leakage boundaries must be enforced by stage-specific visibility
- multiple-testing risk must be tracked with attempt budgets and audit trails
- `no trade` is a first-class research outcome, not missing data

### Server Sandbox Boundaries

The `server` sandbox remains deterministic and replay-driven.

- replay advancement and fill application must persist atomically per step
- `controller/` only handles HTTP translation
- `service/` orchestrates business flow
- `sandbox/` owns replay / clock / matcher logic and must not depend on Gin or GORM
- `model/store/` owns persistence models only

### Live Price Direction

Task 6 work was paused before implementation so live-price access could be redesigned safely.

- live external pricing should not leak into sandbox replay pricing
- future live-price reads should go through a queue-driven in-memory manager
- the subsystem should use one buffer pool, one deduplicated queue, controlled workers, GC, and internal status APIs
- request-path code must not directly hit provider APIs
- existing logger should be reused instead of building a new logging subsystem

## Runtime and Environment Changes

The following project-level runtime support was added or corrected:

- `server/.env.example` created as runtime example
- `server/.env` created for local execution
- `.gitignore` updated to ignore runtime-only artifacts
- `ROOT_USER_EMAIL` loading path fixed so root-user initialization does not silently miss config
- SQLite runtime changed to a pure-Go driver so local startup no longer depends on CGO

## AGENT.md Changes

`AGENT.md` was updated with project-specific operating rules:

- every new conversation must first read `docs/plans/`, then the relevant `server/` and/or `brain/` folders
- every completed task that affects `server/` must verify startup with `cd server && go run main.go`
- task close-out must report completion state, verification commands, and test logic when tests were changed

## Server Sandbox Tasks Completed

The following tasks from `docs/plans/2026-03-09-server-sandbox-implementation-plan.md` are complete:

### Task 1

- finished bootstrapping and route wiring
- server starts cleanly
- `/api/v1/healthz` and `/api/v1/time` are available
- added reusable API smoke coverage in `test/api_smoke.py`

### Task 2

- introduced feed abstraction and deterministic replay fixtures
- separated replay feed from live Binance feed
- avoided `sandbox -> controller` coupling

### Task 3

- implemented run session persistence and stage-specific data visibility boundaries
- locked future data from execution-stage reads
- persisted run metadata required for lineage and audit

### Task 4

- introduced persistence models for market and trading state
- added migration coverage for fresh and upgrade paths

### Task 5-1

- implemented deterministic replay clock and engine state coordination
- kept clock / engine logic free of Gin and GORM

### Task 5-2

- implemented order lifecycle orchestration, deterministic matching, slippage, wallet updates, ledger writes, and transactional rollback guarantees
- preserved atomicity for advance-and-fill steps

## Verification Completed During Server Task Work

The following verification pattern was used repeatedly during implemented server tasks:

- `cd server && go test ./...`
- `cd server && go run main.go`
- `python test/api_smoke.py`

Additional targeted tests were added for:

- boot routes
- replay feed determinism
- data visibility boundaries
- migration correctness
- replay engine reset / sequencing
- order fill, cancel, insufficient balance, and rollback behavior

## Live Price Buffer Planning Status

Two dedicated documents were added for the live-price subsystem:

- `docs/plans/2026-03-09-server-live-price-buffer-design.md`
- `docs/plans/2026-03-09-server-live-price-buffer-implementation-plan.md`

That implementation plan now also ends with:

- a complete appendix of current third-party API touchpoints
- final replacement rules for old provider access paths
- mandatory regression requirements before any live-price replacement is considered finished

Tracked third-party API touchpoints:

- `server/sandbox/live_binance_feed.go`: Binance REST price lookup
- `server/utils/price.go`: Binance websocket miniTicker cache
- `server/utils/utils.go`: Discord webhook and generic external file download
- `server/utils/updater.go`: GitHub releases API and asset download
- `brain/`: current scan found no active third-party API call sites

## Current Project State

### Completed

- design / plan restructuring for `server` and `brain`
- runtime env setup
- server sandbox implementation through Task 5-2
- live-price subsystem design and implementation plan
- third-party API appendix and final regression requirements added to the live-price plan

### Paused

- `server` sandbox Task 6 and later
- live-price subsystem implementation

## Important Open Work

The most important unfinished work after this session:

1. resume `server` sandbox Task 6 and later only after deciding whether any market-facing endpoints should use the new live-price manager
2. implement the live-price buffer plan task-by-task
3. replace direct request-path usage of old live provider helpers only through the new manager boundary
4. keep all existing sandbox and server regressions green while replacing any legacy live-price path

## Notes About Git State

An attempt to stash changes was interrupted because Git safe-directory protection blocked repository access under the sandbox user. No stash or commit should be assumed from this session unless it is verified later.
