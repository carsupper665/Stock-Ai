# Crypto Sandbox Program Implementation Overview

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement the split plans with review checkpoints.

**Goal:** Build a deterministic crypto sandbox in `server/` and a bounded strategy-research system in `brain/` without regressing the current Go project baseline.

**Architecture:** The implementation is now split because `server` owns deterministic market execution and data visibility, while `brain` owns strategy DSL validation, bounded search, decision logging, and deterministic evaluation gates. The integration point between them is a strict API and artifact contract.

**Tech Stack:** Go 1.24 + Gin + GORM + SQLite/Postgres; Python 3.11+; file artifacts with explicit metadata; deterministic evaluation code plus LLM prompts.

---

## Execution Order

1. Execute the server plan first.
2. Execute the brain plan second.
3. Run one integrated sample strategy after both plans are stable.

This order is mandatory because the brain-side workflow depends on server-side replay control, execution metadata, and data-firewall guarantees.

## Shared Checkpoints

- Before implementation:
  - `cd server && go test ./...`
- After every server task batch:
  - `cd server && go test ./...`
- After every brain task batch:
  - `python -m pytest brain/tests`
- After integration batches:
  - `GET /api/v1/healthz`
  - deterministic replay smoke check
  - one sample run produces metadata-complete artifacts

## Shared Non-Regression Rules

- Existing `server/test/price_test.go` must keep passing unless intentionally replaced by a reviewed migration.
- The server must not leak future-segment data through any API.
- Replay advancement and fill application on the server must be persisted atomically per step.
- The brain must not emit hypotheses outside the registered DSL and mutation budget.
- All accepted and rejected hypotheses must remain auditable.

## Split Plans

- Server plan: [2026-03-09-server-sandbox-implementation-plan.md](./2026-03-09-server-sandbox-implementation-plan.md)
- Brain plan: [2026-03-09-brain-strategy-research-implementation-plan.md](./2026-03-09-brain-strategy-research-implementation-plan.md)

## Suggested Batch Sequence

1. Server Tasks 1-3
2. Server Task 4 and Task 5-1
3. Server Task 5-2 and Task 6
4. Server Task 7
5. Brain Tasks 1-3
6. Brain Tasks 4-6
7. Brain Tasks 7-8
8. Integrated sample strategy validation

Each batch should stop for review before the next one starts.
