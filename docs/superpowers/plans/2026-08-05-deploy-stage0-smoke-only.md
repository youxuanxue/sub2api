# Stage0 Deploy Smoke-Only Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a non-mutating `smoke-only` operation to the existing prod deploy workflow so release 1.8.133 can receive canonical smoke evidence without another container recreation or cutover.

**Architecture:** Keep one workflow entry point but split execution into two jobs. The existing `deploy` job retains AWS OIDC and all current behavior; a new read-only `smoke-only` job binds the same `prod` Environment and invokes only the existing gateway smoke and SSOT display gate.

**Tech Stack:** GitHub Actions YAML, Bash smoke runners, Python `unittest` workflow contract tests.

## Global Constraints

- `operation` is a required choice with exactly `deploy` and `smoke-only`, default `deploy`.
- Existing dispatches that omit `operation` continue to deploy.
- Smoke-only receives `contents: read` only; no AWS OIDC, packages permission, SSM, Docker, Caddy, runtime sync, release notification, or rollback.
- Both jobs serialize through `deploy-stage0-prod` concurrency.
- This implementation does not dispatch a workflow or mutate prod.

---

### Task 1: Lock the workflow security and behavior contract

**Files:**
- Create: `ops/stage0/test_deploy_stage0_workflow.py`
- Modify: `.github/workflows/deploy-stage0.yml`
- Modify: `docs/approved/deploy-stage0-smoke-only-mode.md`

**Interfaces:**
- Consumes: existing `ops/stage0/post_deploy_smoke.sh`, `ops/observability/endpoint-compat-audit.sh`, prod Environment secrets and vars.
- Produces: workflow input `operation`; jobs `deploy` and `smoke-only`.

- [ ] **Step 1: Write the failing contract tests**

Create text-structure tests that extract top-level YAML job blocks and assert the operation input, per-job permissions, canonical smoke calls, canonical prod URL fallback, and absence of mutation capabilities from `smoke-only`.

- [ ] **Step 2: Run the test to verify RED**

Run:

```bash
python3 -m unittest ops.stage0.test_deploy_stage0_workflow -v
```

Expected: failures because the operation input and smoke-only job do not exist.

- [ ] **Step 3: Implement the minimal workflow change**

Add the choice input, move OIDC/package permissions to the deploy job, gate deploy on `operation == 'deploy'`, and add the read-only `smoke-only` job with secret validation, full smoke, SSOT display gate, and an accurate non-deploy summary.

- [ ] **Step 4: Run focused tests to verify GREEN**

Run:

```bash
python3 -m unittest \
  ops.stage0.test_deploy_stage0_workflow \
  ops.stage0.test_deploy_via_ssm_bluegreen -v
```

Expected: all tests pass.

- [ ] **Step 5: Run static workflow and repository gates**

Run:

```bash
python3 -m unittest scripts.checks.test_pricing_registry_publication -v
bash scripts/preflight.sh
```

Expected: exit 0. If unrelated baseline failures occur, report them without masking or changing unrelated files.

- [ ] **Step 6: Review and prepare the implementation checkpoint**

Run `git diff --check`, inspect the exact workflow diff, and request review. Stop for approval before commit, push, PR creation, merge, or workflow dispatch.
