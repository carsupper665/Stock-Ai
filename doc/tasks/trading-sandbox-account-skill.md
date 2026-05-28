# Trading Sandbox Account Skill

## Scope

- [x] Keep the account skill focused on the agent trading plane.
- [x] Cover sandbox time, market reads, account reads, order reads, order placement, and order cancellation.
- [x] Exclude strategy generation, replay admin control, dataset management, live execution policy, and backtest expansion.

## Files

- [x] Review `brain/trading-sandbox-account/SKILL.md`.
- [x] Review related backend routes only to confirm endpoint names and request shapes.
- [x] Update this task doc as implementation checklists are completed.

## Implementation Steps

- [x] Confirm the skill lists the approved endpoints for sandbox time, market data, account state, performance, orders, positions, and trades.
- [x] Confirm create and cancel order guidance matches the backend API shape.
- [x] Remove or avoid guidance that turns the skill into a strategy, replay, live trading, or dataset admin spec.
- [x] Keep examples short enough for an agent to run as a smoke test.

## Tests

- [x] Run `C:\Program Files\Git\bin\bash.exe ./test/trading_sandbox_account_skill_test.sh`.
- [x] Verify the skill documents token login, sandbox time, bounded market reads, order create, order cancel, and account state endpoint strings.
- [x] Verify required account-token scopes and forbidden-scope terms are documented as contract guardrails only.
- [x] Record the expected contract-test command output: `PASS`.

## Acceptance

- [x] The skill documents only the allowed trading-plane actions.
- [x] The skill gives clear commands or request shapes for the smoke path.
- [x] The acceptance commands for the implementation task pass before this module is marked complete in `doc/tasks/progress.md`.


