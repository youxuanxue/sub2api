---
title: Ops Unified Contract (P0 + QA + Cron Agent)
status: approved
approved_by: youxuanxue
approved_at: 2026-04-21
created: 2026-04-21
owners: [tk-platform]
related_prs: ["#13"]
---

# Ops Unified Contract

This document is the single source of truth for TokenKey ops capability.
It replaces and supersedes:

- `ops-p0-observability.md`
- `ops-qa-full-capture.md`
- `ops-cron-agent-workflow.md`

All new ops changes must align to this contract first, then implementation.

## 1) Product Principle (Jobs)

- Focus first: prioritize the smallest end-to-end loop that creates real operator value.
- One intent, one path: avoid duplicate endpoints and duplicated UI journeys.
- Preserve existing user-visible capabilities: do not silently remove upstream or already-online features.
- Prefer clear defaults over extra knobs unless a knob has an active operator use case.

## 2) Engineering Principle (OPC)

- Automation over ritual: recurring checks must run in workflow/preflight, not by memory.
- Reliability over sophistication: graceful degradation beats brittle "perfect path".
- Minimize operational surface area: avoid adding systems unless current systems are proven bottlenecks.
- Keep merge friction low: fork-only additions and thin upstream hook points.

## 3) Canonical Scope

### 3.1 Ops monitoring baseline (P0)

Required baseline:

- Structured JSON logs with redaction.
- Prometheus metrics endpoint wired and protected.
- Preflight gates in local hook + CI.
- Admin ops runtime switch in settings (`ops_monitoring_enabled`).

### 3.2 QA full capture

Target contract:

- Capture gateway QA records with metadata + blob storage flow.
- User-scoped QA export and delete APIs.
- Monthly export and partition/archive workflows.

Runtime compatibility rule:

- If `qa_records` is not deployed yet, QA cron workflows must exit cleanly (skip), not fail.

### 3.3 Cron + agent loop

Minimum automation loop:

- `error-clustering-daily` for persistent clusters.
- `weekly-product-pulse` for KPI and trend summary.
- `agent-draft-pr` outputs draft PR only (never auto-merge).

Cooldown and guardrails must remain enabled:

- Cluster-signature cooldown.
- Protected-path diff guard.
- Preflight pass before draft PR creation.

## 4) Current Implementation Alignment Rules

### 4.1 Backward compatibility

- Existing API behavior must not be broken by optional parameter tightening.
- Example: QA export keeps backward-compatible default `format=zip` when omitted.

### 4.2 No capability trimming

- Do not remove online/upstream capabilities in ops routes or admin pages.
- Improvements must be additive, stabilizing, or de-duplicating behind compatibility windows.

### 4.3 De-duplication policy

- Legacy and split paths may coexist only during migration windows.
- New code should prefer canonical split paths; legacy paths stay until explicit deprecation plan lands.

## 5) Required Mechanical Checks

- `python3 scripts/export_agent_contract.py --check`
- `./scripts/preflight.sh`
- Workflow-level safeguards for ops cron jobs:
  - missing secret => skip (non-fatal)
  - missing required table => skip (non-fatal)

## 6) Decision Matrix (Jobs + OPC)

When evaluating an ops change:

1. Does it preserve existing online capability? If no, reject.
2. Does it reduce operator time-to-decision or time-to-fix? If no, reject.
3. Is there already another path for same intent? If yes, converge instead of adding.
4. Can failure degrade to safe skip/retry rather than hard stop? If no, redesign.

## 7) Immediate Execution Baseline

The branch is considered aligned when all are true:

- Ops monitoring remains enabled and authenticated.
- QA cron jobs skip cleanly when `qa_records` is not yet present.
- Weekly pulse KPI generation is automated and has local dry-run verification.
- No change in this branch silently removes existing upstream/online ops features.