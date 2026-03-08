# Crypto Sandbox Program Design Overview

This revision incorporates the latest design feedback and follows the `superpowers` design workflow:

- `brainstorming`: make constraints explicit before implementation
- `writing-plans`: turn the design into bounded, testable work
- `executing-plans`: implement in reviewed batches instead of one long speculative pass

The design is now split into separate `server` and `brain` documents because the two sides have different risks, validation rules, and implementation cadence.

## Shared Design Corrections

### 1. `brain` must be a constrained research kernel, not a polished reporter

The previous shape made `brain` too close to a journaling and review assistant. That is useful, but not sufficient. The new design makes `brain` responsible for deterministic strategy representation, bounded candidate generation, and deterministic acceptance gates. The LLM is only one component inside that system.

### 2. Strategy mutation must stay inside a legal search space

The project now treats strategy design as a bounded DSL problem, not free-form prompt output. The search space must explicitly define:

- which rule families exist
- which parameters are mutable
- which modules may be toggled
- how many modules may change in one hypothesis
- which changes require human approval

This prevents the common failure mode where the LLM either becomes too free or too constrained to be useful.

### 3. Data leakage must be blocked by architecture, not trust

The design now explicitly separates:

- what data is visible during strategy drafting
- what data is visible during bar-by-bar execution
- when in-sample summaries are unlocked
- when OOS summaries are unlocked
- when a final acceptance decision may be computed

`brain` must not see future bars, full-run summaries, or OOS metrics before the candidate strategy is frozen.

### 4. Multiple testing must be treated as a first-class risk

The system now assumes repeated hypothesis testing will happen and must be controlled. Each strategy lineage needs:

- a candidate budget per round
- immutable storage for rejected hypotheses
- attempt counters
- stricter acceptance after repeated trials
- optional final locked holdout when dataset size allows

This prevents "careful-looking overfitting".

### 5. Improvement must use a deterministic acceptance function

The system must not let `brain` optimize whichever metric looks best in a given run. A deterministic acceptance function is required:

- hard gates for risk and robustness
- minimum trade-count checks
- cost-stress survival checks
- ranking only after the gates pass

### 6. File artifacts need lineage metadata from day one

File-based artifacts are still acceptable in phase 1, but only if every artifact carries enough metadata to reconstruct lineage and validation context. Required metadata now includes:

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

### 7. "No trade" is a first-class outcome

The system must log not only trades, but also the absence of trades. The design therefore separates:

- `decision_log.jsonl`: every decision opportunity, including no-trade and blocked-trade reasons
- `journal.jsonl`: trade-focused post-trade analysis for executed trades

This avoids a bias toward learning only from bars where a trade happened.

## Document Split

- Server design: [2026-03-09-server-sandbox-design.md](./2026-03-09-server-sandbox-design.md)
- Brain design: [2026-03-09-brain-strategy-research-design.md](./2026-03-09-brain-strategy-research-design.md)

## Implementation Split

- Server plan: [2026-03-09-server-sandbox-implementation-plan.md](./2026-03-09-server-sandbox-implementation-plan.md)
- Brain plan: [2026-03-09-brain-strategy-research-implementation-plan.md](./2026-03-09-brain-strategy-research-implementation-plan.md)

## Recommended Build Order

1. Build the `server` deterministic sandbox and data-visibility controls first.
2. Build the `brain` DSL, mutation controls, and evaluation gates second.
3. Integrate them only after both sides can be tested independently.

The rest of the details now live in the split documents above.
