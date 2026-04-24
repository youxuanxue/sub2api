---
title: Cloud-Agent-Driven Tag-and-Deploy Workflow
status: pending
approved_by: youxuanxue (PR #53 squash-merge)
approved_at: 2026-04-24
created: 2026-04-23
owners: [tk-platform]
related_prs: []
# NOTE: deliberately empty while status=pending to satisfy R3.
# Status flips to `shipped` only after first successful deploy via the
# new workflow; at that point add ["#53", "<first-deploy-commit>"] here.
# `approved_by` was flipped at merge time (PR #53) to satisfy R5
# (main/master禁止 approved_by: pending).
scope: ".github/workflows/deploy-stage0.yml + IAM scope expansion in deploy/aws/cloudformation/cicd-oidc.yaml"
---

# Cloud-Agent-Driven Tag-and-Deploy Workflow

## 1. Why this exists

The release loop "tag → `release.yml` builds image → operator runs SSM SOP
from a workstation → verify health" today requires an operator-side step
that no cloud agent can take: the existing `AWS_OIDC_ROLE_ARN` trust
policy only accepts the GitHub Actions OIDC issuer, not a cloud-agent VM.
Every release is therefore gated on a human pasting the multi-line
`aws ssm send-command` block from `deploy/aws/README.md` § 生产升级 SOP,
even though the command is mechanical and identical between releases.

This proposal adds **one** workflow `.github/workflows/deploy-stage0.yml`
that wraps that SOP. The cloud-agent loop becomes:

```
bash scripts/release-tag.sh vX.Y.Z                                 # existing
gh workflow run deploy-stage0.yml -f environment=test -f tag=X.Y.Z # NEW
gh workflow run deploy-stage0.yml -f environment=prod -f tag=X.Y.Z # NEW (gated by Environment reviewer)
```

The workflow only **automates the keystrokes** the operator already runs
by hand. No new AWS infrastructure, no behavior change to `release.yml`
or the stage0 stack, no new SSM Documents.

## 2. Why this is high-risk

Per `product-dev.mdc` §高风险 — prod-touching automation that:

- **Mutates durable host state**: rewrites `/var/lib/tokenkey/.env` and
  restarts the prod `tokenkey` container.
- **Expands a security boundary** (Section 3): adds an optional test
  instance + the `environment:prod` / `environment:test` OIDC subjects to
  the existing role.
- **Has high blast radius**: a wrong tag, an arch-mismatched image
  (`simple_release=true` amd64-only on Graviton), or an unhealthy
  container after restart all surface as immediate API outage on
  `api.tokenkey.dev`.

What stops these risks from materialising lives in Section 4 (workflow
shape) and Section 5 (operator setup); each item is a hard mechanical
gate, not a convention.

## 3. IAM trust expansion

`deploy/aws/cloudformation/cicd-oidc.yaml` — additive only:

| Field | Before | After |
|---|---|---|
| `AllowedSubjects` default | `repo:youxuanxue/sub2api:ref:refs/heads/main` | adds `environment:prod` and `environment:test` |
| `TargetInstanceId` (prod) | scalar, default `i-04a8afd18c997b8ac` | unchanged |
| `TestTargetInstanceId` (test) | absent | new optional scalar; empty default suppresses the test IAM statement via `HasTestInstance` condition |
| `cloudformation:DescribeStacks` resource | `tokenkey-prod-stage0/*` | adds `tokenkey-test-stage0/*` |
| `ssm:SendCommand` resource | `AWS-RunShellScript` only | unchanged (still no `ec2:`, `iam:`, `s3:`) |
| Role name | `tokenkey-gha-${AWS::Region}-error-clustering` | unchanged (back-compat with `vars.AWS_OIDC_ROLE_ARN` consumers) |

`error-clustering-daily.yml` and `prod-log-dump.yml` continue to work
unchanged because the `main` branch subject is preserved.

## 4. Workflow shape

`.github/workflows/deploy-stage0.yml` — `workflow_dispatch` only. No
schedule, no auto-fire on tag push.

Inputs:

| Name | Type | Default | Notes |
|---|---|---|---|
| `environment` | choice `test|prod` | required | selects stack name **and** binds the OIDC subject |
| `tag` | string | required | image tag without leading `v`; must match `^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$` |
| `simple_release_override` | bool | `false` | flip only when the target host is amd64 (default-deny against the §9.1 Graviton trap) |

Steps:

1. **Validate `tag` regex + resolve stack name** (defaults
   `tokenkey-{prod,test}-stage0`, overridable via repo vars
   `PROD_STACK_NAME` / `TEST_STACK_NAME`).
2. **GHCR multi-arch manifest precheck** — fetch
   `https://ghcr.io/v2/${repo}/manifests/${tag}`, require a manifest list
   containing both `linux/amd64` and `linux/arm64` descriptors. Fail-closed
   unless `simple_release_override=true`. This is the §9.1 trap rebuilt as
   a hard gate at deploy time.
3. **Configure AWS credentials via OIDC** — `aws-actions/configure-aws-credentials@v4`,
   role from `vars.AWS_OIDC_ROLE_ARN`. The job-level
   `environment: ${{ inputs.environment }}` binding (a) adds the subject
   the IAM trust requires, (b) pauses for any reviewer rule configured on
   that Environment (Section 5).
4. **Resolve target instance + api domain** from the stack's
   `InstanceId` / `ApiUrl` outputs.
5. **Deploy via SSM Run-Command** — same commands as
   `deploy/aws/README.md` § 生产升级 SOP, transported via
   `aws ssm send-command --parameters file://…`:
   `.env` snapshot → `sed` image tag → `docker compose pull tokenkey` →
   `up -d --no-deps tokenkey` → 12 × 5 s health-poll → `compose ps` →
   `docker logs --since 2m | tail -20`. Job fails if the container does
   not reach `healthy`.
6. **External health-check** — `curl ${ApiUrl}/health`, three attempts
   spaced 10 s apart, require HTTP 200 within 5 s.
7. **Job summary** — write the deployed tag, the SSM command id, and a
   one-liner re-dispatch command for rollback. No auto-rollback (would
   mask transient failures).

Concurrency `group: deploy-stage0-${{ inputs.environment }}`,
`cancel-in-progress: false`. Permissions `contents: read`,
`id-token: write`, `packages: read`. No `contents: write`.

## 5. Required pre-deploy operator setup

After this PR merges, before the first dispatch:

1. **Update the IAM stack**:
   ```bash
   aws cloudformation deploy --region us-east-1 \
     --stack-name tokenkey-cicd-oidc \
     --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
     --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides TestTargetInstanceId=<test-instance-id>
   ```
   `AllowedSubjects` already defaults to the broader set; no override
   needed. Pass `TestTargetInstanceId=` empty if test deploys are not
   wanted yet.

2. **Create GitHub Environments** in repo Settings → Environments:
   - `prod`: enable **Required reviewers** (yourself) + a small **Wait
     timer** (e.g. 60 s).
   - `test`: no protection rules needed.

   GitHub auto-creates Environments on first reference — so this step is
   what *adds the reviewer gate*, not what makes the workflow runnable.
   **Skipping it means prod deploys run unattended.**

3. **(Optional) Override repo variables** if defaults don't fit:
   `vars.PROD_STACK_NAME`, `vars.TEST_STACK_NAME`, `vars.AWS_REGION`.

## 6. Explicitly out of scope

To stay on "automate the existing manual SOP" and nothing else:

- **No DB migrations / schema bumps** — only restarts the `tokenkey`
  container; PostgreSQL / Redis / Caddy are untouched.
- **No multi-region** — role scoped to one `AWS::Region`.
- **No automatic test → prod promotion** — operator (or cloud agent on
  operator instructions) explicitly dispatches each environment.
- **No auto-rollback** — re-dispatch the workflow with the previous tag.
- **No CFN `ImageTag` parameter mutation** — drift between the CFN
  parameter and runtime `TOKENKEY_IMAGE` remains the accepted trade-off
  documented in `deploy/aws/README.md` §升级 / 发版.

## 7. Rollback of this PR itself

If the workflow misbehaves after merge:

- **Disable**: Settings → Actions → "Stage0 Deploy" → Disable. Operators
  fall back to the manual SOP in `deploy/aws/README.md` §生产升级 SOP
  (kept intact for this case).
- **Revert IAM**: re-deploy `cicd-oidc.yaml` with `TestTargetInstanceId=""`
  and `AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main"`.
  Role ARN does not change, so `error-clustering-daily.yml` /
  `prod-log-dump.yml` are unaffected.
- **No data migration** — nothing in this PR writes durable state.

## 8. Acceptance criteria

After merge + operator setup, the PR is acceptable when both adversarial
gates fire correctly and the regression check holds:

1. **Manifest precheck (Step 2 above) is fail-closed**: dispatching with a
   non-existent `tag` (e.g. `99.99.99`), or with a single-arch tag from a
   `simple_release=true` build, exits the run **before** any SSM command
   is sent.
2. **Existing OIDC consumers unaffected**: `error-clustering-daily.yml`
   and `prod-log-dump.yml` next runs after the IAM stack update succeed
   (regression check on the trust expansion).

A successful deploy itself is not a separate acceptance bullet — that
*is* the workflow's purpose, observed via job-summary HTTP 200 from
Step 6.

## 9. Status

- [ ] Proposal merged (this PR)
- [ ] IAM stack redeployed with `TestTargetInstanceId`
- [ ] GitHub Environments `prod` (with reviewer) + `test` created
- [ ] First successful test deploy via `gh workflow run`
- [ ] First successful prod deploy via `gh workflow run`
- [ ] Status flipped to `shipped` with merge PR + first-deploy commit in
      `related_prs` / `related_commits`
