# Brain Strategy Research Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a constrained strategy-research runtime in `brain/` that uses a registered DSL, bounded mutation space, decision logging, and deterministic acceptance gates instead of free-form performance chasing.

**Architecture:** The `brain` runtime is split into a deterministic research kernel and an LLM-assisted layer. The deterministic layer owns schema validation, experiment budgets, acceptance rules, and artifact lineage. The LLM is limited to structured reasoning inside those boundaries.

**Tech Stack:** Python 3.11+, JSON/JSONL artifacts, prompt templates, deterministic test suite.

---

### Task 1: Create the Brain Skeleton and Test Harness

**Files:**

- Create: `brain/main.py`
- Create: `brain/tests/test_smoke.py`
- Create: `brain/tests/__init__.py`
- Create: `brain/artifacts/.gitkeep`

**Objective:**

Make `brain/` a real executable workspace instead of an empty placeholder.

**Steps:**

1. Add a small CLI entry for `run`, `audit`, and `hypothesis`.
2. Create a minimal test harness so future tasks have a stable baseline.
3. Ensure artifact output paths are created safely.

**Checkpoint:**

- `python brain/main.py --help`
- `python -m pytest brain/tests`

### Task 2: Implement Strategy DSL and Module Registry

**Files:**

- Create: `brain/schemas/strategy_dsl.py`
- Create: `brain/core/module_registry.py`
- Create: `brain/tests/test_strategy_dsl.py`

**Objective:**

Define the legal strategy surface before any LLM behavior is added.

**Steps:**

1. Register allowed module families and parameter ranges.
2. Define which fields are mutable and which require human approval.
3. Reject unknown modules, unknown parameters, and out-of-range values.
4. Add tests for valid and invalid strategy specs.

**Checkpoint:**

- invalid specs are rejected deterministically
- legal SNR/FIBO specs are accepted
- no arbitrary indicators can be injected

### Task 3: Implement Mutation Space and Experiment Budget Controls

**Files:**

- Create: `brain/core/mutation_space.py`
- Create: `brain/core/experiment_registry.py`
- Create: `brain/schemas/hypothesis.py`
- Create: `brain/tests/test_mutation_space.py`

**Objective:**

Prevent the system from becoming a careful-looking overfitter.

**Steps:**

1. Generate legal candidate mutations only from the DSL registry.
2. Enforce one structural change per hypothesis.
3. Track `attempt_index`, `parent_strategy_id`, and per-round mutation budget.
4. Store accepted and rejected hypotheses in an append-only registry.

**Checkpoint:**

- illegal mutations are rejected
- attempt budgets are enforced
- rejected hypotheses remain queryable

### Task 4: Implement Sandbox Client and Visibility Guard

**Files:**

- Create: `brain/clients/sandbox_client.py`
- Create: `brain/core/visibility_guard.py`
- Create: `brain/tests/test_client_visibility.py`

**Objective:**

Make the client refuse future-leaking workflows even if the caller is careless.

**Steps:**

1. Build a client for run creation, replay advancement, market reads, and order APIs.
2. Add client-side assertions for run-stage visibility.
3. Refuse to consume summaries or metrics before the run stage permits it.
4. Test normal and forbidden read flows with mocked server responses.

**Checkpoint:**

- client can consume server responses in the allowed order
- client refuses early OOS or future-segment access

### Task 5: Implement Decision Logging and Trade Journaling

**Files:**

- Create: `brain/schemas/decision_log.py`
- Create: `brain/schemas/journal_entry.py`
- Create: `brain/workflows/run_strategy.py`
- Create: `brain/workflows/audit_run.py`
- Create: `brain/tests/test_decision_logging.py`

**Objective:**

Make no-trade and blocked-trade outcomes auditable.

**Steps:**

1. Define `decision_log.jsonl` schema with `enter`, `exit`, `hold`, `skip`, and `blocked`.
2. Define reason codes for no-trade and risk-block states.
3. Generate `journal.jsonl` for executed trades.
4. Add tests proving no-trade cases are logged and preserved.

**Checkpoint:**

- `decision_log.jsonl` includes skip and blocked events
- `journal.jsonl` includes only executed trades
- missing no-trade reasons fail tests

### Task 6: Implement Deterministic Evaluation and Acceptance Gates

**Files:**

- Create: `brain/core/acceptance.py`
- Create: `brain/workflows/evaluate_run.py`
- Create: `brain/tests/test_acceptance.py`

**Objective:**

Define improvement in code, not by LLM preference.

**Steps:**

1. Implement hard gates for OOS, drawdown, turnover, cost stress, and minimum trade count.
2. Implement deterministic ranking for candidates that pass the gates.
3. Add stricter thresholds after repeated failed attempts.
4. Test acceptance and rejection on fixed metric fixtures.

**Checkpoint:**

- acceptance results are deterministic
- a candidate cannot pass by improving only one vanity metric
- repeated attempts tighten acceptance

### Task 7: Add Prompt Contracts for Structured LLM Use

**Files:**

- Create: `brain/prompts/strategy_system.txt`
- Create: `brain/prompts/journal_auditor.txt`
- Create: `brain/prompts/hypothesis_generator.txt`
- Create: `brain/tests/test_prompt_contracts.py`

**Objective:**

Constrain the LLM to the legal research role.

**Steps:**

1. Write prompts that require DSL-conformant outputs.
2. Force hypothesis outputs to include exact changed field, expected benefit, side effect, and validation checks.
3. Explicitly forbid new indicators, new logic blocks, and hidden metric changes.
4. Add tests or fixture validations against the expected JSON shape.

**Checkpoint:**

- prompt outputs can be validated against schemas
- hypothesis outputs without validation steps are rejected

### Task 8: Add Artifact Manifest and End-to-End Brain Run

**Files:**

- Create: `brain/schemas/manifest.py`
- Create: `brain/core/artifacts.py`
- Create: `brain/tests/test_artifacts.py`
- Modify: `可用提示詞.txt`

**Objective:**

Make every run reconstructable and ready for integration with the sandbox.

**Steps:**

1. Define `manifest.json` with the required metadata keys.
2. Write artifact helpers that emit:
   - `manifest.json`
   - `strategy_spec.json`
   - `run_request.json`
   - `decision_log.jsonl`
   - `journal.jsonl`
   - `metrics.json`
   - `hypothesis.json`
3. Run one fixture-driven end-to-end research loop using an example such as SNR + FIBO.
4. Verify the registry and artifact set are complete.

**Checkpoint:**

- artifacts are lineage-complete
- one example research run completes from DSL spec to hypothesis
- `python -m pytest brain/tests`
