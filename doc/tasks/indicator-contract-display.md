# Indicator Contract Display

## Scope

- [x] Define and display sandbox snapshot indicator data in the frontend.
- [x] Keep the first UI pass focused on visible contract support and clear empty states.
- [x] Exclude a full K-line framework, strategy signals, autonomous trading, and backtest expansion.

## Files

- [x] Review backend indicator response shape and snapshot payload fields.
- [x] Review frontend sandbox snapshot types.
- [x] Review sandbox admin and monitor views that render market context.
- [x] Update this task doc as implementation checklists are completed.

## Implementation Steps

- [x] Complete backend indicator contract keys for SMA, MACD, RSI, Bollinger, OBV, KD/KDJ, DMI/ADX, AD, and BIAS.
- [x] Keep backend indicator JSON finite by omitting unavailable non-finite points from emitted series.
- [x] Add indicator fields to frontend domain types using the backend contract.
- [x] Preserve indicator payloads when snapshots enter the frontend store.
- [x] Render available indicators in the sandbox admin or monitor surface selected by the implementation task.
- [x] Show a clear empty or partial indicator state when payloads are missing.

## Tests

- [x] Add backend tests for requested indicator keys and no-NaN JSON encoding.
- [x] Add `test/indicator_contract_test.sh` shell wrapper that prints PASS on success and exits nonzero on failure.
- [x] Add frontend tests for snapshots that include indicator payloads.
- [x] Add frontend tests for empty or partial indicator payloads.
- [x] Run frontend test and build commands listed by the implementation task.

## Acceptance

- [x] Backend snapshot indicator contract exposes all requested indicator keys.
- [x] Backend indicator JSON encoding contains no NaN or Inf values.
- [x] Snapshot indicators are present in the frontend type system.
- [x] Indicator payloads are not dropped by stores or views.
- [x] A user can see whether indicator data exists for the selected sandbox context.
- [x] The acceptance commands for the implementation task pass before this module is marked complete in `doc/tasks/progress.md`.
