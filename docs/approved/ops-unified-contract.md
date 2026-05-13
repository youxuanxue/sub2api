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

## 3. Core Capability B: Prod/Edge OpsToIssue

Required outcomes:

- `Prod Ops` runs daily diagnostics for prod plus every deployable Edge target from `deploy/aws/stage0/edge-targets.json`.
- Runtime findings are normalized into `ops-report.{json,md}` and can create/update GitHub issue signals.
- Optional Claude diagnosis may read the aggregate report and repository files to improve issue quality; it must not write files, run shell commands, access AWS, create branches, or create PRs.

Hard guardrails:

- Issue only: no automatic PR creation from `Prod Ops`.
- Signature cooldown / dedupe labels (`ops-sig:*`, plus `cluster-sig:*` for error clusters) avoid duplicate churn.
- AWS diagnostics jobs have `id-token: write` but no repo write permissions.
- Issue/Claude jobs have no AWS OIDC permission.
- Missing optional secret / missing required table => clean skip or deterministic fallback, not a brittle cron failure.

Transport (since 2026-05-13):

- Workflow discovers targets as prod + deployable Edge matrix entries. Planned Edge targets remain excluded until `deployable=true` and IAM/SSM setup are complete.
- Per-target diagnostics use GitHub OIDC → AWS STS → SSM Run-Command. PostgreSQL is **not** exposed to the public internet; error clustering still connects to `tokenkey-postgres` via the target's docker network from a transient `alpine:3.21` container.
- IaC: `deploy/aws/cloudformation/cicd-oidc.yaml`. Setup SOP:
  `deploy/aws/README.md` § "CI 通过 OIDC 调度 SSM".
- Graceful skip path keys on `vars.AWS_OIDC_ROLE_ARN` and runtime table availability. `qa_records` absence returns a `skip:` report and does not fail the run.

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
- Prod/Edge diagnostics can flow to issue signals with AWS/issue permissions separated.
