# Trading Sandbox Gap TODO

## Current Read

- Backend core is mostly in place for sandbox/account/order/replay/realtime monitor.
- Current agent skill is close to the right scope for a minimal trading skill: market read, account read, order create/read/cancel, no strategy logic.
- Main gaps are in frontend realtime account wiring, indicator exposure/visualization, multi-account end-to-end validation, and working-tree cleanliness.

## Skill Scope

- [ ] Keep `brain/trading-sandbox-account/SKILL.md` focused on agent trading plane only.
  - Keep: `GET /sandbox/time`, `GET /market/*`, `GET /account`, `GET /account/performance`, `GET /orders`, `GET /positions`, `GET /trades`, `POST /orders`, `POST /orders/:id/cancel`
  - Do not add strategy generation, replay admin control, dataset management, or live execution policy into this skill.
- [ ] If needed, split a second doc for "backend implementation checklist" so the current skill remains a smoke-test/runbook instead of becoming a giant spec.

## P0 - Must Close First

- [ ] Wire frontend realtime account updates to `/ws/account`.
  - Current backend already exposes `GET /ws/account` and sends account snapshot + account events.
  - Current frontend only consumes admin monitor websocket and rebuilds `accountSeries` from sandbox snapshots.
  - Goal: per-account balance/equity/open-order updates should change without waiting for full sandbox snapshot refresh.

- [ ] Add true multi-account sandbox integration coverage.
  - Current backend logic processes pending orders by sandbox and then refreshes all accounts in that sandbox.
  - Add tests for 2+ virtual accounts in one sandbox placing orders concurrently and verify independent position/equity/order state.

- [ ] Bring indicator implementation into tracked source control.
  - `server/service/indicators.go`
  - `server/service/indicators_test.go`
  - These files currently exist in working tree but are untracked.

## P1 - Product Completeness

- [ ] Expose `snapshot.indicators` to frontend type system.
  - Update frontend domain types so sandbox snapshot can carry indicators.
  - Update stores/views to retain indicator payload instead of silently dropping it.

- [ ] Render indicator data in sandbox/admin/monitor UI.
  - At minimum show K-line context plus selectable overlays/subcharts.
  - Suggested first batch: MA/SMA, MACD, RSI, Bollinger, OBV.

- [ ] Add missing technical indicators requested by product scope.
  - Missing or not evidenced in current implementation: KD/KDJ, DMI/ADX, AD, BIAS/乖離率.
  - Current backend indicator code only evidences: SMA, MACD, RSI, Bollinger Bands, OBV.

- [ ] Define a stable indicator response contract.
  - Naming convention for series keys
  - Lookback/window metadata
  - Timestamp alignment rules
  - Replay-time cutoff rules

- [ ] Add frontend indicator visibility tests.
  - Snapshot with indicators rendered in admin sandbox detail
  - Snapshot with indicators rendered in monitor sandbox view
  - Empty/partial indicator payload behavior

## P1 - Replay/Sandbox UX

- [ ] Make replay-mode data visibility more explicit in UI.
  - Show dataset coverage window next to chart data requests.
  - Show that candle/indicator visibility is capped by sandbox current time.
  - Make missing-data state explicit instead of looking like "no chart".

- [ ] Add a sandbox trading verification path.
  - Current backend supports sandbox order placement.
  - Current UI is mainly admin/monitor oriented and does not provide a simple human-triggered sandbox trade console.
  - Decide whether this should be an admin debug form, a dedicated QA page, or remain agent-only.

## P2 - Quality / Operability

- [ ] Normalize `test/*.sh` line endings.
  - Current shell scripts fail under bash with `set: pipefail\r: invalid option name`.
  - Backend Go tests still pass directly, but the script delivery is not shell-clean yet.

- [ ] Clean up dirty working tree before claiming indicator/monitor completeness.
  - Many backend files are modified.
  - Indicator support currently depends on untracked files.

- [ ] Add a pass/fail checklist for repo readiness.
  - Backend: `go test ./...`
  - Frontend: `npm test`, `npm run build`
  - Agent plane: token login + sandbox time + kline cutoff + order create/cancel + account/trade/position verification

## Notes

- Raw OHLCV data already supports a large portion of the requested indicator set.
- The bigger missing piece is not raw dataset shape; it is computed-indicator coverage plus frontend consumption.
- Order API shape is already close to trading-system best practice: create/list/get/cancel is more appropriate than generic CRUD update/delete for live trading semantics.
