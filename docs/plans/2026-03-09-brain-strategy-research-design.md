# Brain Strategy Research Design

## Purpose

`brain/` is not just a report writer. It is a constrained research module made of deterministic code plus LLM-assisted reasoning. Its job is to represent strategies in a bounded DSL, generate legal hypotheses, log decisions, and evaluate candidates under strict anti-overfitting rules.

## Architecture Principle

The `brain` runtime has two layers:

### Deterministic research kernel

This layer is trusted and implemented as normal code:

- strategy DSL validation
- legal mutation-space generation
- experiment budget tracking
- artifact metadata and lineage tracking
- acceptance function evaluation
- data-visibility stage enforcement on the client side

### LLM-assisted layer

This layer is useful, but not authoritative:

- draft or refine a strategy spec inside the DSL
- explain trade reasoning in structured form
- summarize violations
- propose one bounded hypothesis chosen from the legal mutation set

The LLM must not invent new strategy primitives, new metrics, or new acceptance rules.

## Strategy DSL Boundary

The DSL must explicitly register the allowed strategy primitives. Phase 1 should not support arbitrary indicator growth.

### Registered module families

- entry setup
  - `snr_touch`
  - `fibo_retrace`
  - `snr_fibo_retest`
- filters
  - `trend_filter`
  - `volatility_filter`
  - `cooldown_filter`
  - `session_filter`
- exits
  - fixed stop
  - fixed take profit
  - R-multiple target
  - time stop
- sizing
  - fixed notional
  - fixed risk percent
- risk caps
  - max risk per trade
  - max daily loss
  - max concurrent exposure

### Allowed mutable space

The legal mutation engine may change only:

- entry threshold values
- exit threshold values
- sizing rule parameters
- cooldown length
- risk-cap values
- on/off state of optional registered filters

### Forbidden mutation space

The legal mutation engine must reject:

- arbitrary new indicators
- arbitrary new logic blocks
- unlimited stacking of conditions
- symbol-universe changes without human approval
- timeframe changes without human approval
- cost-model changes
- data-split changes

### Structural mutation rule

One hypothesis may introduce at most one structural change:

- enable one registered filter
- disable one registered filter
- swap one registered exit style

All other changes in the same hypothesis must be parameter tuning within that structure.

## Legal Hypothesis Space

The hypothesis system must be explicit about legality. Every hypothesis must include:

- `hypothesis_id`
- `parent_strategy_id`
- exact rule or parameter being changed
- expected improvement
- possible side effect
- required validation checks

If a proposal cannot be mapped to registered DSL fields, it is invalid by definition.

## Data Leakage Controls

`brain` must not accidentally consume future information through artifacts or summary timing.

### Visibility stages

- Strategy drafting:
  - may use approved training schema and approved training context only
- Bar-by-bar execution:
  - may only see data available up to the current cursor
- Decision logging:
  - must be generated online or reconstructed strictly from prefix-only information
- In-sample review:
  - allowed only after candidate freeze
- OOS review:
  - allowed only after OOS completion
- Promotion review:
  - only if the optional final holdout is unlocked

### Artifact timing rule

`decision_log.jsonl` and `journal.jsonl` must be written in chronological order or from a replay that preserves event-time visibility. The brain must never first read a full-run summary and then pretend to journal earlier decisions.

## Multiple Testing Controls

The design must treat repeated experiments as a source of false confidence.

### Required controls

- each strategy lineage has `attempt_index`
- each lineage has a `hypothesis_budget_per_round`
- all accepted and rejected hypotheses are stored immutably
- acceptance thresholds become stricter after repeated failed attempts
- optional locked final holdout is recommended when data size allows

### Recommended phase 1 defaults

- maximum 3 candidate mutations per parent strategy per round
- one accepted child per round
- after the budget is exhausted, human review is required before another round

## Acceptance Function

Improvement must be deterministic and code-defined.

### Hard gates

A candidate fails immediately if any of these fail:

- OOS profit factor below configured minimum
- cost-stress expectancy not positive
- max drawdown worsens beyond configured limit
- turnover increases beyond configured limit
- sample size is below minimum trade count
- new auditor rule-violation flags appear

### Ranking after gates

Only candidates that pass the gates are ranked. Recommended ranking:

1. highest median walk-forward OOS expectancy
2. then highest stability score
3. then lowest turnover increase

The LLM must not choose a different ranking rule at runtime.

## Decision Logging and No-Trade Support

`brain` must treat "do nothing" as an explicit decision, not missing data.

### `decision_log.jsonl`

Each decision opportunity should log:

- `run_id`
- `strategy_id`
- `bar_time`
- `decision_type`: `enter`, `exit`, `hold`, `skip`, `blocked`
- `triggered_rules`
- `skip_reason_code`
- `block_reason_code`
- `risk_budget_state`
- `evidence_snapshot`

### `journal.jsonl`

Trade-focused records should log:

- entry reason
- executed rules
- risk / sizing basis
- invalidation condition
- actual fill and slippage
- post-trade commentary

`journal.jsonl` is not enough by itself. `decision_log.jsonl` is the primary behavioral record.

## Artifact and Lineage Model

Phase 1 may still use file artifacts, but each run must produce a manifest and be indexable.

### Required files per run

- `manifest.json`
- `strategy_spec.json`
- `run_request.json`
- `decision_log.jsonl`
- `journal.jsonl`
- `metrics.json`
- `hypothesis.json`

### Required manifest metadata

- `strategy_id`
- `parent_strategy_id`
- `hypothesis_id`
- `run_id`
- `scenario_id`
- `dataset_hash`
- `execution_model_version`
- `fee_model_version`
- `slippage_model_version`
- `artifact_timestamp`

### Registry

Phase 1 should keep an append-only registry such as `registry.jsonl` so lineage and rejection history are queryable even before a DB-backed artifact store exists.

## Proposed Code Layout

```text
brain/
  main.py
  clients/
    sandbox_client.py
  prompts/
    strategy_system.txt
    journal_auditor.txt
    hypothesis_generator.txt
  schemas/
    strategy_dsl.py
    decision_log.py
    journal_entry.py
    hypothesis.py
    manifest.py
  core/
    module_registry.py
    mutation_space.py
    experiment_registry.py
    acceptance.py
    artifacts.py
    visibility_guard.py
  workflows/
    run_strategy.py
    audit_run.py
    evaluate_run.py
    propose_hypothesis.py
  tests/
    test_strategy_dsl.py
    test_mutation_space.py
    test_acceptance.py
    test_workflows.py
  artifacts/
    .gitkeep
```

## Testing Strategy

### Deterministic tests

- reject invalid DSL specs
- reject illegal mutations
- enforce hypothesis budget
- compute acceptance decisions identically on repeated inputs

### Workflow tests

- decision log includes `skip` and `blocked` outcomes
- journal generation cannot run from future-leaking input
- rejected hypotheses remain queryable

### Contract tests

- `brain` handles server responses that include version metadata
- `brain` refuses responses missing required run metadata

## Design Decisions Deferred

- whether module weights or parameter priors should be added later
- whether the registry remains file-based after MVP or moves to DB
- whether a human approval step is required for every structural mutation or only risky ones
