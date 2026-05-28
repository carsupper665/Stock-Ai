# Account Realtime WebSocket

## Scope

- [x] Wire frontend account state to `/ws/account` for realtime balance, equity, open order, position, and trade updates.
- [x] Keep existing admin monitor websocket behavior intact.
- [x] Avoid adding strategy, signal, live execution, or backtest behavior.

## Files

- [x] Review frontend websocket or API client code that reads sandbox account state.
- [x] Review stores and views that render balance, equity, orders, positions, and trades.
- [x] Review backend `/ws/account` response shape before changing frontend types.
- [x] Update this task doc as implementation checklists are completed.

## Implementation Steps

- [x] Add or reuse a frontend account websocket client for `/ws/account`.
- [x] Feed account snapshots and account events into the current sandbox account store path.
- [x] Keep full sandbox snapshot refresh as a broader state source, not the only account update path.
- [x] Handle account selection so updates apply to the correct virtual account.

## Tests

- [x] Add or update frontend tests for websocket account snapshot handling.
- [x] Add or update frontend tests for incremental account events.
- [x] Cover at least one account switch or account-id routing case if the store supports multiple accounts.

## Acceptance

- [x] Account balance and equity can update without waiting for a full sandbox snapshot refresh.
- [x] Open order, position, and trade state stays scoped to the intended account.
- [x] The acceptance commands for the implementation task pass before this module is marked complete in `doc/tasks/progress.md`.

## UI Integration

- [x] `SandboxDetailView` connects visible sandbox accounts to the account realtime store.
- [x] `SandboxDetailView` prefers matching realtime account row and equity data over stale sandbox snapshots.
- [x] `SandboxMonitorView` connects ranked sandbox accounts to the account realtime store.
- [x] `SandboxMonitorView` prefers matching realtime account ranking and chart data over stale sandbox snapshots.
- [x] Existing admin monitor socket and fallback polling behavior remain in place.
