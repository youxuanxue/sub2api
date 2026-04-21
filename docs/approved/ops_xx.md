---
title: Ops Unified Contract (QA + ErrorToIssue/PR)
status: approved
approved_by: youxuanxue
approved_at: 2026-04-21
created: 2026-04-21
owners: [tk-platform]
related_prs: ["#13"]
scope: "QA capture + ErrorToIssue/PR"
---

# Ops Unified Contract

Single source of truth for Ops in this repo.

## 1. Non-Negotiables

### 1.1 Jobs focus

- Keep only capabilities that shorten operator decision/fix loop.
- One intent, one canonical path; avoid parallel surfaces for the same action.
- Do not trim existing online or upstream-visible functionality.

### 1.2 OPC leverage

- Automate checks in workflow/preflight; do not rely on memory.
- Favor graceful degradation (`skip`) over brittle hard failure in cron pipelines.
- Keep merge conflict surface minimal (fork-only modules + thin upstream hook points).

## 2. Core Capability A: 100% QA Capture

Required outcomes:

- QA requests/responses are captured with metadata and blob references.
- Monthly QA maintenance workflows exist (export / partition / archive).

Runtime rule:

- If `qa_records` is not yet deployed in a target environment, QA workflows must **skip cleanly** and not fail the run.

Compatibility rule:

- API changes must not break existing callers by tightening optional parameters without fallback.
- QA export keeps backward-compatible default `format=zip` when omitted.

## 3. Core Capability B: ErrorToIssue/PR

Required outcomes:

- Daily clustering (`error-clustering-daily`) can create/update issue signals.
- Agent action can draft PRs from persistent clusters.
- Weekly pulse reports KPI snapshots for review cadence.

Hard guardrails:

- Draft PR only (never auto-merge).
- Signature cooldown to avoid duplicate churn.
- Protected-path diff guard before PR creation.
- `./scripts/preflight.sh` must pass before draft PR creation.

## 4. Merge-Safe Alignment Rules

- No capability trimming: preserve currently online/admin/upstream behavior.
- De-duplication is allowed only with compatibility window (legacy path remains until explicit retirement plan lands).
- Prefer additive or stabilizing changes over broad rewrites in upstream-owned files.

## 5. Mechanical Gates

Must stay green:

- `python3 scripts/export_agent_contract.py --check`
- `./scripts/preflight.sh`
- `./scripts/weekly-product-pulse-dry-run.sh`

Workflow resilience baseline:

- Missing secret => skip (non-fatal)
- Missing required table => skip (non-fatal)

## 6. Acceptance Checklist

Branch is aligned when:

- Existing online/upstream capabilities remain available.
- QA workflows degrade safely when `qa_records` is absent.
- Error clustering can flow to issue/draft-PR with guardrails intact.
- Weekly KPI generation remains automated and dry-run verifiable.