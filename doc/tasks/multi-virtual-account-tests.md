# Multi Virtual Account Tests

## Scope

- [x] Add coverage for two or more virtual accounts inside one sandbox.
- [x] Verify independent order, position, equity, and account state after concurrent or overlapping activity.
- [x] Exclude strategy logic, signal generation, live execution, and broad backtest coverage.

## Files

- [x] Review backend sandbox, account, order, position, and trade service tests.
- [x] Review existing scripts under `test/` before adding any new script in the implementation task.
- [x] Update this task doc as implementation checklists are completed.

## Implementation Steps

- [x] Create or update backend tests that open two accounts in one sandbox.
- [x] Place orders for both accounts and process them through the sandbox flow.
- [x] Assert each account keeps its own orders, positions, trades, balance, and equity.
- [x] Add a shell script only if the implementation task changes API or service behavior and the repo rule requires it.

## Tests

- [x] Run focused backend tests for the sandbox multi-account flow.
- [x] Run the required shell script if one is created or touched.
- [x] Confirm any shell script prints PASS on success and exits nonzero on failure.

## Acceptance

- [x] Two or more virtual accounts can trade in the same sandbox without state bleed.
- [x] Backend tests cover independent account state after order processing.
- [x] Required shell-script delivery rules are met for API or service changes.
- [x] The acceptance commands for the implementation task pass before this module is marked complete in `doc/tasks/progress.md`.
