---
title: Ops Unified Contract (QA + ErrorToIssue/PR)
status: approved
approved_by: youxuanxue
approved_at: 2026-04-21
created: 2026-04-21
owners: [tk-platform]
related_prs: ["#13", "#30"]
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

Runtime rule:

- If `qa_records` is not yet deployed in a target environment, QA-dependent jobs must **skip cleanly** and not fail the run.

Compatibility rule:

- API changes must not break existing online callers.

## 3. Core Capability B: ErrorToIssue/PR

Required outcomes:

- Daily clustering (`error-clustering-daily`) can create/update issue signals.
- Agent action can draft PRs from persistent clusters.

Hard guardrails:

- Draft PR only (never auto-merge).
- Signature cooldown to avoid duplicate churn.
- Protected-path diff guard before PR creation.
- `./scripts/preflight.sh` must pass before draft PR creation.

Transport (since 2026-04-22):

- Workflow runs the clustering binary on the prod EC2 host via AWS SSM
  Run-Command, authenticated by GitHub OIDC. PostgreSQL is **not** exposed
  to the public internet; the binary connects to `tokenkey-postgres` via the
  `tokenkey_default` docker network from a transient `alpine:3.21` container.
- IAM trust scope: single role per repo+branch (default `main` only),
  permitted only `ssm:SendCommand` against the prod EC2 instance and the
  `AWS-RunShellScript` document.
- IaC: `deploy/aws/cloudformation/cicd-oidc.yaml`. Setup SOP:
  `deploy/aws/README.md` § "CI 通过 OIDC 调度 SSM".
- Graceful skip path now keys on `vars.AWS_OIDC_ROLE_ARN` instead of the
  legacy `secrets.PROD_PG_READONLY_DSN`; same shape (empty → exit 0 with
  `skip:` summary), no behavior regression for environments not yet wired.

## 4. Merge-Safe Alignment Rules

- No capability trimming: preserve currently online/admin/upstream behavior.
- De-duplication is allowed only with compatibility window (legacy path remains until explicit retirement plan lands).
- Prefer additive or stabilizing changes over broad rewrites in upstream-owned files.

## 5. Mechanical Gates

Must stay green:

- `python3 scripts/export_agent_contract.py --check`
- `./scripts/preflight.sh`

Workflow resilience baseline:

- Missing secret => skip (non-fatal)
- Missing required table => skip (non-fatal)

## 6. Acceptance Checklist

Branch is aligned when:

- Existing online/upstream capabilities remain available.
- QA workflows degrade safely when `qa_records` is absent.
- Error clustering can flow to issue/draft-PR with guardrails intact.