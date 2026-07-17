---
title: Cloud-Agent-Driven Tag-and-Deploy Workflow
status: shipped
approved_by: youxuanxue (PR #53 squash-merge)
approved_at: 2026-04-24
created: 2026-04-23
shipped_at: 2026-04-24
owners: [tk-platform]
related_prs: ["#53", "#976", "#978"]
# First successful prod deploy via the new workflow:
#   GHA run https://github.com/youxuanxue/sub2api/actions/runs/24872412714
#   (env=prod, tag=1.6.0, no-op image hash, external /health 200).
# Adversarial fail-closed gate also verified:
#   GHA run https://github.com/youxuanxue/sub2api/actions/runs/24872388875
#   (tag=99.99.99 → exited at GHCR manifest precheck before any AWS call).
scope: ".github/workflows/deploy-stage0.yml (prod-only) + ops/stage0/deploy_via_ssm_bluegreen.sh + ops/stage0/post_deploy_smoke.sh + scripts/checks/bluegreen-migration-safety.py + IAM in deploy/aws/cloudformation/cicd-oidc.yaml"
---

# Cloud-Agent-Driven Tag-and-Deploy Workflow

## 1. Why this exists

The release loop is now: tag → `release.yml` builds a multi-arch image →
operator dispatches one prod Environment-gated workflow → the workflow performs
the same-host blue/green SSM deploy, external health check, and gateway smoke.

This document originally approved the cloud-agent wrapper around the old manual
SSM SOP. The 2026-06-24 revision keeps the same GitHub Environment/OIDC control
plane, but changes the prod host mutation from single-container restart to
same-host blue/green, single data layer. The operator loop remains:

```
bash scripts/release-tag.sh vX.Y.Z                                 # existing
gh workflow run deploy-stage0.yml -f tag=X.Y.Z                     # NEW (gated by prod Environment reviewer)
```

No new AWS infrastructure or SSM Document is introduced. The behavior change is
limited to runtime state on the existing prod EC2 host: two app colors,
`active-color`, generated blue/green compose, blue/green `tokenkey.service`, and
Caddy active-upstream rewrites.

## 2. Why this is high-risk

Per `product-dev.mdc` §高风险 — prod-touching automation that:

- **Mutates durable host state**: rewrites `/var/lib/tokenkey/.env`, writes
  blue/green compose and `active-color`, installs a blue/green
  `tokenkey.service`, rewrites the live Caddy upstream, and starts/stops app
  color containers.
- **Expands a security boundary** (Section 3): adds the `environment:prod`
  OIDC subject to the existing role (prod Stage0 deploy only).
- **Has high blast radius**: a wrong tag, an arch-mismatched image
  (`simple_release=true` amd64-only on Graviton), incompatible DB migration,
  bad Caddy upstream rewrite, or an unhealthy target color after cutover can
  surface as API errors on `api.tokenkey.dev`.

What stops these risks from materialising lives in Section 4 (workflow
shape) and Section 5 (operator setup); each item is a hard mechanical
gate, not a convention.

## 3. IAM trust expansion

`deploy/aws/cloudformation/cicd-oidc.yaml` — additive only:

| Field | Before | After |
|---|---|---|
| `AllowedSubjects` default | `repo:youxuanxue/sub2api:ref:refs/heads/main` | adds `environment:prod` (and Edge subjects when present in template) |
| `TargetInstanceId` (prod) | scalar, default `i-04a8afd18c997b8ac` | unchanged |
| `cloudformation:DescribeStacks` resource | `tokenkey-prod-stage0/*` | unchanged for prod-only deploy |
| `ssm:SendCommand` resource | `AWS-RunShellScript` only | unchanged (still no `ec2:`, `iam:`, `s3:`) |
| Role name | `tokenkey-gha-${AWS::Region}-error-clustering` | unchanged (back-compat with `vars.AWS_OIDC_ROLE_ARN` consumers) |

`ops-daily-diagnostics.yml` continues to cover both error clustering and prod log dump
because the `main` branch subject is preserved.

## 4. Workflow shape

`.github/workflows/deploy-stage0.yml` — `workflow_dispatch` only. No
schedule, no auto-fire on tag push.

Inputs:

| Name | Type | Default | Notes |
|---|---|---|---|
| `tag` | string | required | image tag without leading `v`; must match `^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$` |
| `simple_release_override` | bool | `false` | flip only when the target host is amd64 (default-deny against the §9.1 Graviton trap) |

The job always binds GitHub Environment **`prod`** (OIDC subject `environment:prod`).

Steps:

1. **Validate `tag` regex + resolve stack name** (default
   `tokenkey-prod-stage0`, overridable via repo var `PROD_STACK_NAME`).
2. **GHCR multi-arch manifest precheck** — fetch
   `https://ghcr.io/v2/${repo}/manifests/${tag}`, require a manifest list
   containing both `linux/amd64` and `linux/arm64` descriptors. Fail-closed
   unless `simple_release_override=true`. This is the §9.1 trap rebuilt as
   a hard gate at deploy time.
3. **Configure AWS credentials via OIDC** — `aws-actions/configure-aws-credentials@v6`,
   role from `vars.AWS_OIDC_ROLE_ARN`. The job-level **`environment: prod`**
   binding (a) adds the subject the IAM trust requires, (b) pauses for any
   reviewer rule configured on the prod Environment (Section 5).
4. **Blue/green migration safety gate** — compare the target release tag to
   the previous release tag (fallback `origin/main..HEAD` only when the target
   tag cannot be resolved locally) and scan changed SQL migrations for
   old-code-incompatible patterns. `DROP`, `RENAME`, `SET NOT NULL`,
   `ADD COLUMN ... NOT NULL`, and `ALTER ... TYPE` require an explicit
   `bluegreen-safe-destructive-ok` acknowledgement after expand/contract
   review. This is fail-closed before AWS credentials are configured.
5. **Resolve target instance + api domain** from the stack's
   `InstanceId` / `ApiUrl` outputs.
6. **Deploy via SSM Run-Command** — call
   `ops/stage0/deploy_via_ssm_bluegreen.sh` against the prod EC2 `i-*`
   instance. The primitive is prod-only; Lightsail Edge keeps the single-app
   `deploy_via_ssm.sh` path.

   First run self-migrates the legacy app:
   `.env` backup → derive current `tokenkey` image → write
   `/var/lib/tokenkey/docker-compose.bluegreen.yml` → start
   `tokenkey-blue` with the current image → wait Docker health and `/health`
   readiness → rewrite only the live Caddy `reverse_proxy` upstream to
   `tokenkey-blue:8080` and hot reload → write
   `/var/lib/tokenkey/active-color=blue` → install the blue/green
   `tokenkey.service` → drain and remove legacy `tokenkey`.

   Every subsequent deploy alternates colors:
   `.env` backup → pull target tag into the inactive color
   (`tokenkey-blue` or `tokenkey-green`) → start/recreate only that color →
   wait Docker health and `/health` readiness → rewrite only the live Caddy
   upstream to the target color and hot reload → atomically write
   `active-color` → install/update the blue/green systemd unit → SIGUSR1/drain
   and stop the previous color.

   PostgreSQL, Redis, Caddy, `/var/lib/tokenkey/app`, and the Docker network
   remain the single shared data layer. The generated blue/green compose only
   contains the two app services and points them at `tokenkey-postgres` /
   `tokenkey-redis`.
7. **External health-check** — `curl ${ApiUrl}/health`, three attempts
   spaced 10 s apart, require HTTP 200 within 5 s.
8. **Post-deploy live-host advisory checks** — the workflow reads the active
   app container and drift-sensitive env on the host, and also runs the
   exclusive-group orphan check. These checks warn but do not roll back a
   successfully switched color.
9. **Sync Feishu alert config** — fail the deploy if prod cannot persist and
   verify the shared Feishu alert webhook/secret.
10. **Post-deploy gateway smoke** — `ops/stage0/post_deploy_smoke.sh` against
   `${ApiUrl}`: public settings, authenticated `/v1/models`,
   `/v1/chat/completions`, and `/v1/messages` (Claude Code-style `x-api-key`).
   Requires **`prod` Environment secret** `TK_SMOKE_API_KEY` (one all-capability
   user `sk-...` valid on that stack). Fail-closed if the secret is missing,
   if any configured smoke model is absent from `/v1/models`, or if any step
   returns non-200 / unexpected body markers.
   Model-list vars: `TK_SMOKE_ANTHROPIC_MODELS` (default
   `claude-sonnet-4-6`), `TK_SMOKE_GEMINI_MODELS` (default empty; native
   Gemini Google One pool retired 2026-07-04), `TK_SMOKE_OPENAI_OAUTH_MODELS`
   (default `gpt-5.4`).
11. **Job summary** — write the deployed tag, the SSM command id, and a
   one-liner re-dispatch command for rollback. No auto-rollback (would
   mask transient failures).

Concurrency `group: deploy-stage0-prod`,
`cancel-in-progress: false`. Permissions `contents: read`,
`id-token: write`, `packages: read`. No `contents: write`.

## 5. Required pre-deploy operator setup

After this PR merges, before the first dispatch:

1. **Update the IAM stack** (drop any legacy `TestTargetInstanceId` overrides from older templates):

   ```bash
   aws cloudformation deploy --region us-east-1 \
     --stack-name tokenkey-cicd-oidc \
     --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
     --capabilities CAPABILITY_NAMED_IAM
   ```

   `AllowedSubjects` defaults include `environment:prod` and Edge environments as shipped in the template.

2. **Create GitHub Environment** in repo Settings → Environments:

   - `prod`: enable **Required reviewers** (yourself) + a small **Wait timer** (e.g. 60 s).

   GitHub auto-creates Environments on first reference — so this step is
   what *adds the reviewer gate*, not what makes the workflow runnable.
   **Skipping it means prod deploys run unattended.**

3. **(Optional) Override repo variables** if defaults don't fit:
   `vars.PROD_STACK_NAME`, `vars.AWS_REGION`.

4. **`prod` Environment smoke config** — configure in GitHub Settings →
   Environments → `prod`:
   - `TK_SMOKE_API_KEY` — all-capability gateway smoke key (`sk-...`)
   - `TK_SMOKE_ANTHROPIC_MODELS` — Anthropic/chat+messages model list
   - `TK_SMOKE_GEMINI_MODELS` — optional native Gemini schema probe model list;
     leave empty unless a new native Gemini pool is provisioned and live-probed.
   - `TK_SMOKE_OPENAI_OAUTH_MODELS` — OpenAI OAuth probe model list
   The deploy workflow fails if the key is unset or a listed model is not visible to it. See `deploy/aws/README.md`
   (Smoke config).

## 6. Explicitly out of scope

To stay focused on prod deploy automation and nothing else:

- **No general-purpose DB migration framework** — app startup may apply
  normal migrations, but the workflow only adds a static blue/green
  compatibility gate for changed SQL. Destructive/contract migrations still
  require manual expand/contract review and explicit acknowledgement.
- **No multi-region** — role scoped to one `AWS::Region`.
- **No separate staging promotion gate inside Actions** — `deploy-stage0.yml`
  upgrades **`prod` only**; smoke probes target that stack's `ApiUrl`.
- **No post-cutover auto-rollback** — before Caddy reload, failures leave the
  old color untouched and serving; after Caddy reload, re-dispatch the workflow
  with the previous tag so the rollback goes through the same health/smoke path.
- **No CFN `ImageTag` parameter mutation** — drift between the CFN
  parameter and runtime `TOKENKEY_IMAGE` remains the accepted trade-off
  documented in `deploy/aws/README.md` §升级 / 发版.

## 7. Rollback of this PR itself

If the workflow misbehaves after merge:

- **Disable**: Settings → Actions → "Stage0 Deploy" → Disable. Operators
  fall back only after explicitly choosing a recovery path for the live host
  layout: either keep blue/green and run `deploy_via_ssm_bluegreen.sh` manually,
  or restore the legacy single-app compose/service and then use the manual SOP
  in `deploy/aws/README.md` §生产升级 SOP.
- **Revert IAM**: re-deploy `cicd-oidc.yaml` from this repo revision or tighten
  `AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main"` if needed.
  Role ARN does not change, so `ops-daily-diagnostics.yml` diagnostics are unaffected.
- **Durable host state** — after the blue/green revision, prod deploys can leave
  `/var/lib/tokenkey/docker-compose.bluegreen.yml`,
  `/var/lib/tokenkey/active-color`, per-color image env keys, and a blue/green
  `tokenkey.service`. To disable the workflow without rolling the host layout
  back, re-dispatch the previous known-good tag through the same workflow.

## 8. Acceptance criteria

After merge + operator setup, the PR is acceptable when both adversarial
gates fire correctly and the regression check holds:

1. **Manifest precheck (Step 2 above) is fail-closed**: dispatching with a
   non-existent `tag` (e.g. `99.99.99`), or with a single-arch tag from a
   `simple_release=true` build, exits the run **before** any SSM command
   is sent.
2. **Existing OIDC consumers unaffected**: `ops-daily-diagnostics.yml` error clustering
   and log dump runs after the IAM stack update succeed (regression check on
   the trust expansion).

A successful deploy itself is not a separate acceptance bullet — that
*is* the workflow's purpose, observed via job-summary HTTP 200 from
Step 6.

## 9. Status

- [x] Proposal merged (PR #53, 2026-04-24)
- [x] GitHub Environment `prod` created with Required reviewers
- [x] **`deploy-stage0.yml` prod-only** — GitHub `environment=test` path removed (template defaults dropped `environment:test` OIDC subject and test-instance IAM stub).
- [x] First successful prod deploy via `gh workflow run` —
      [run 24872412714](https://github.com/youxuanxue/sub2api/actions/runs/24872412714)
      (env=prod, tag=1.6.0, external `/health` HTTP 200)
- [x] Status flipped to `shipped` (this PR)
- [x] 2026-06-24 revision: prod deploy primitive changed from single-container
      restart to same-host blue/green, single data layer. PR #976 shipped the
      runtime change; PR #978 hardened the approved baseline, Caddy active
      upstream handling, SSM timeout, and migration-safety guard.

### Adversarial gate verified

The fail-closed manifest precheck (Section 4 step 2 / Section 8 acceptance
#1) was confirmed by
[run 24872388875](https://github.com/youxuanxue/sub2api/actions/runs/24872388875):
dispatched with `tag=99.99.99`, exited at the GHCR manifest precheck
step **before** any AWS credential was configured or SSM command sent.
