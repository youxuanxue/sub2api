---
title: Cloud-Agent-Driven Tag-and-Deploy Workflow
status: pending
approved_by: pending
approved_at: pending
created: 2026-04-23
owners: [tk-platform]
related_prs: []
# NOTE: deliberately empty while status=pending to satisfy R3.
# After merge + first successful deploy, flip status=shipped and
# add ["#53", "<first-deploy-commit>"] here.
scope: ".github/workflows/deploy-stage0.yml + IAM scope expansion in deploy/aws/cloudformation/cicd-oidc.yaml"
---

# Cloud-Agent-Driven Tag-and-Deploy Workflow

## 1. Why this exists

Today the loop "tag → release.yml builds image → operator runs SSM SOP from a
workstation → verify health" requires an operator-side step that no cloud
agent can take: the existing `AWS_OIDC_ROLE_ARN` trust policy only accepts
the GitHub Actions OIDC issuer, not a cloud-agent VM. Every release is
therefore gated on a human pasting the multi-line `aws ssm send-command`
block from `deploy/aws/README.md` § 生产升级 SOP, even though the command
is mechanical and identical between releases.

This proposal adds **one** workflow `.github/workflows/deploy-stage0.yml`
that wraps that SOP. The cloud-agent loop then becomes:

```
bash scripts/release-tag.sh vX.Y.Z                                 # existing
gh workflow run deploy-stage0.yml -f environment=test -f tag=X.Y.Z # NEW
gh workflow run deploy-stage0.yml -f environment=prod -f tag=X.Y.Z # NEW (gated by Environment reviewer)
```

The workflow only **automates the keystrokes** the operator already runs by
hand. No new infrastructure, no behavior change to `release.yml` or the
stage0 stack, no SSM Document authoring.

## 2. Why this is high-risk

Per `product-dev.mdc` §高风险 — prod-touching automation that:

- **Mutates durable host state**: rewrites `/var/lib/tokenkey/.env` and
  restarts the prod `tokenkey` container. A bug in the workflow can cause
  prod outage.
- **Expands a security boundary**: the existing OIDC role is locked to one
  prod EC2 instance + the `tokenkey-prod-stage0` stack pattern + the `main`
  branch subject. This proposal adds an optional test instance + the
  `environment:prod` / `environment:test` OIDC subjects (Section 3).
- **Has high blast radius**: a wrong tag, an arch-mismatched image
  (`simple_release=true` amd64-only on Graviton ARM), or an unhealthy
  container after restart all surface as immediate API outage on
  `api.tokenkey.dev`.

Mitigations baked into the workflow (Section 4) match the dev-rules
"no soft rule without a hard check" mandate: dispatch-only trigger, strict
input grammar, multi-arch manifest precheck, GitHub Environment binding as
the human-in-the-loop gate, automatic `.env` snapshot before mutation.

## 3. IAM trust expansion

`deploy/aws/cloudformation/cicd-oidc.yaml` is updated additively:

| Field | Before | After |
|---|---|---|
| `AllowedSubjects` default | `repo:youxuanxue/sub2api:ref:refs/heads/main` | adds `environment:prod` and `environment:test` |
| `TargetInstanceId` (prod) | scalar, default `i-04a8afd18c997b8ac` | unchanged |
| `TestTargetInstanceId` (test) | absent | new optional scalar; empty default suppresses the test IAM statement via `HasTestInstance` condition |
| `cloudformation:DescribeStacks` resource | `tokenkey-prod-stage0/*` only | adds `tokenkey-test-stage0/*` |
| `ssm:SendCommand` action | unchanged | unchanged (still only `AWS-RunShellScript`; no `ec2:`, `iam:`, `s3:`) |
| Role name | `tokenkey-gha-${AWS::Region}-error-clustering` | unchanged (back-compat with `vars.AWS_OIDC_ROLE_ARN` consumers) |

The `main` branch subject is preserved so `error-clustering-daily.yml` and
`prod-log-dump.yml` keep working unchanged. The two new
`environment:` subjects are what binds the deploy workflow to the GitHub
Environment reviewer gate (Section 5).

## 4. Workflow shape

`.github/workflows/deploy-stage0.yml` — `workflow_dispatch` only.

Inputs:

| Name | Type | Default | Notes |
|---|---|---|---|
| `environment` | choice `test|prod` | required | selects stack name **and** binds the OIDC subject |
| `tag` | string | required | image tag without leading `v` (e.g. `1.6.0`); must match `^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$` |
| `simple_release_override` | bool | `false` | flip only when target host is amd64 (default-deny against §9.1 Graviton trap) |

Steps (each is a thin wrapper around the documented SOP, no custom logic):

1. **Validate `tag`** — strict regex on the input (the `environment` choice
   type already constrains its values).
2. **Resolve stack name** from `inputs.environment` (defaults
   `tokenkey-{prod,test}-stage0`, overridable via repo vars
   `PROD_STACK_NAME` / `TEST_STACK_NAME`).
3. **Verify GHCR multi-arch manifest** — fetch
   `https://ghcr.io/v2/${repo}/manifests/${tag}`, require a manifest list
   containing both `linux/amd64` and `linux/arm64` descriptors.
   `simple_release_override=true` downgrades this to a warning. Fail-closed
   default — this is the §9.1 trap rebuilt as a hard precheck.
4. **Configure AWS credentials via OIDC** —
   `aws-actions/configure-aws-credentials@v4`, role from
   `vars.AWS_OIDC_ROLE_ARN`.
5. **Resolve target instance + api domain** from the stack outputs
   (`InstanceId`, `ApiUrl`).
6. **Run deploy on EC2 via SSM** — same commands as
   `deploy/aws/README.md` § 生产升级 SOP, transported via
   `aws ssm send-command --parameters file://…`:
   1. backup `.env` → `.env.before-${tag}`
   2. `sed` the image tag in `.env`
   3. `docker compose pull tokenkey`
   4. `docker compose up -d --no-deps tokenkey`
   5. health-poll loop (12 × 5 s); fail if not `healthy` at the end
   6. `docker compose ps`
   7. `docker logs tokenkey --since 2m | tail -20`
7. **External health-check** — `curl ${ApiUrl}/health`, require HTTP 200
   under 5 s, three attempts spaced 10 s apart.
8. **Job summary** — write the deployed tag, the SSM command id, and a
   one-liner rollback dispatch command (same workflow, previous tag).
   No auto-rollback — that would mask transient failures.

Concurrency: `group: deploy-stage0-${{ inputs.environment }}`,
`cancel-in-progress: false` — two operators racing the same environment
queue safely; cross-environment deploys are independent.

Permissions: `contents: read`, `id-token: write`, `packages: read` (for the
GHCR manifest precheck). No `contents: write` — the workflow does not
mutate the repository.

## 5. Required pre-deploy operator setup

After this PR merges, before the first dispatch:

1. **Update the IAM stack** with the new template:
   ```bash
   aws cloudformation deploy --region us-east-1 \
     --stack-name tokenkey-cicd-oidc \
     --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
     --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides TestTargetInstanceId=<test-instance-id>
   ```
   `AllowedSubjects` now defaults to the broader set; no override needed.
   Pass `TestTargetInstanceId=` empty if test deploys are not wanted yet.

2. **Create GitHub Environments** in repo Settings → Environments:
   - `prod`: enable **Required reviewers** (yourself) and a small **Wait
     timer** (e.g. 60 s) so a manual approval click is required before the
     workflow proceeds.
   - `test`: no protection rules needed.

   Note: GitHub silently auto-creates Environments on first reference if
   they don't exist, so step 2 is what *adds the reviewer gate*, not what
   makes the workflow runnable. Without step 2 the prod deploy will run
   unattended — do not skip it.

3. **(Optional) Override repo variables** if defaults don't fit:
   `vars.PROD_STACK_NAME`, `vars.TEST_STACK_NAME`, `vars.AWS_REGION`.

## 6. What this proposal does NOT do

To stay scoped to "automate the existing manual SOP" (Jobs minimal API
surface):

- **No new AWS infrastructure**: reuses `AWS-RunShellScript` like every
  other ops workflow in the repo.
- **No CFN drift management**: the workflow does not touch the stage0
  stack `ImageTag` parameter. Drift between CFN parameter and runtime
  `TOKENKEY_IMAGE` remains an accepted trade-off
  (`deploy/aws/README.md` §升级 / 发版).
- **No DB migrations / schema bumps**: only restarts the `tokenkey`
  container; PostgreSQL / Redis / Caddy are untouched.
- **No multi-region**: the role is scoped to a single `AWS::Region`.
- **No automatic test → prod promotion**: the operator (or the cloud agent
  acting on operator instructions) must explicitly dispatch the prod
  deploy after verifying test.
- **No auto-rollback**: rollback is a re-dispatch with the previous tag —
  same path, same gates, same audit trail.

## 7. Mechanical gates

Per `product-dev.mdc` §"Hard Constraint Wiring":

- **`scripts/preflight.sh`** — already wired. This doc lives in
  `docs/approved/`, so dev-rules `check_approved_docs.py` enforces R1-R4
  on every commit.
- **`tag` input regex** — strict regex enforced as the workflow's first
  step; the workflow refuses to run on malformed `tag`.
- **GHCR multi-arch manifest precheck** — fail-closed Step 3 above. This
  is the mechanical complement to `release.yml`'s `simple_release=false`
  default (CLAUDE.md §9.1).
- **OIDC trust binding to GitHub Environment** — the role assumption
  cannot succeed without the Environment subject, so any protection rules
  configured on `prod` (Required reviewers / Wait timer) are load-bearing
  on the AWS side, not just GitHub UI cosmetic.

## 8. Rollback of this PR itself

If the workflow turns out to misbehave after merge:

- **Disable**: Settings → Actions → Workflows → "Stage0 Deploy" → Disable.
  Operators fall back to the manual SOP in `deploy/aws/README.md`
  §生产升级 SOP — that path is kept intact in the README for exactly this
  case.
- **Revert IAM**: re-deploy `cicd-oidc.yaml` with `TestTargetInstanceId=""`
  and `AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main"`.
  The role ARN does not change, so `error-clustering-daily.yml` /
  `prod-log-dump.yml` are unaffected.
- **No data migration**: nothing in this PR writes durable state, so revert
  is a config rollback only.

## 9. Acceptance criteria

After merge + operator setup:

1. `gh workflow run deploy-stage0.yml -f environment=test -f tag=1.6.0`
   completes successfully and `curl https://test-api.tokenkey.dev/health`
   returns HTTP 200.
2. `gh workflow run deploy-stage0.yml -f environment=prod -f tag=1.6.0`
   prompts the configured reviewer in the prod Environment, then on
   approval completes and `https://api.tokenkey.dev/health` returns 200.
3. Dispatching with a non-existent `tag` (e.g. `99.99.99`) fails at Step 3
   (manifest precheck) **before** any SSM command is sent.
4. Dispatching a tag whose `:TAG` manifest is single-arch (e.g. produced
   by `simple_release=true`) fails at Step 3 unless
   `simple_release_override=true` is also passed.
5. `error-clustering-daily.yml` / `prod-log-dump.yml` continue to work
   unchanged (regression check on the trust expansion).

## 10. Status

- [ ] Proposal merged (this PR)
- [ ] IAM stack redeployed with new defaults + `TestTargetInstanceId`
- [ ] GitHub Environments `prod` (with reviewer) + `test` created
- [ ] First successful test-stack deploy via `gh workflow run`
- [ ] First successful prod-stack deploy via `gh workflow run`
- [ ] Status flipped to `shipped` with merge PR + first-deploy commit
      listed in `related_prs` / `related_commits`
