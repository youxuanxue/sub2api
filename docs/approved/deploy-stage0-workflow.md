---
title: Cloud-Agent-Driven Stage0 Blue/Green Deployment Workflow
status: approved
approved_by: "feng (2026-08-28 revision); youxuanxue (PR #53 initial approval)"
approved_at: 2026-08-28
created: 2026-04-23
shipped_at: 2026-04-24
owners: [tk-platform]
related_prs: ["#53", "#976", "#978"]
revised: 2026-08-28
related_designs:
  - docs/approved/edge-bluegreen-release-safety.md
# First successful prod deploy via the new workflow:
#   GHA run https://github.com/youxuanxue/sub2api/actions/runs/24872412714
#   (env=prod, tag=1.6.0, no-op image hash, external /health 200).
# Adversarial fail-closed gate also verified:
#   GHA run https://github.com/youxuanxue/sub2api/actions/runs/24872388875
#   (tag=99.99.99 → exited at GHCR manifest precheck before any AWS call).
scope: ".github/workflows/deploy-stage0.yml + .github/workflows/deploy-edge-lightsail-stage0.yml + scripts/checks/bluegreen-migration-safety.py + ops/stage0/bluegreen-capacity-policy.env + ops/stage0/deploy_via_ssm_bluegreen.sh + scripts/stage0/pick_release_canary_edge.py + Stage0 smoke and rollout guards"
---

# Cloud-Agent-Driven Stage0 Blue/Green Deployment Workflow

## 1. Why this exists

The release loop is now: tag → `release.yml` builds a multi-arch image →
operator dispatches the Environment-gated prod workflow and the fleet Edge
rollout → both use the same-host blue/green SSM owner, external health checks,
and their canonical smoke suites.

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
| `operation` | choice | `deploy` | `deploy` runs the image switch and acceptance path; `smoke-only` runs canonical API acceptance probes; `qa-infra-check` validates the dedicated QA OIDC/IAM path and stack binding without mutation |
| `tag` | string | required | image tag without leading `v`; must match `^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$`; in read-only modes it is an audit label only |
| `simple_release_override` | bool | `false` | deploy-only; flip only when the target host is amd64 (default-deny against the §9.1 Graviton trap) |

All jobs bind GitHub Environment **`prod`**. The `deploy` job receives
`id-token: write` and `packages: read`; `qa-infra-check` receives only
`contents: read` plus `id-token: write` so it can prove both OIDC role assumptions;
`smoke-only` receives `contents: read` only and cannot obtain AWS credentials.

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
4. **Blue/green migration safety gate** — prod and Edge call
   `scripts/checks/bluegreen-migration-safety.py` before app mutation. The
   checker absorbs the existing prod tag/range preparation as its release-mode
   entry. This centralizes the current gate without redesigning its comparison
   semantics; workflow YAML contains no second implementation.
5. **Resolve target instance + api domain** from the stack's
   `InstanceId` / `ApiUrl` outputs.
6. **Deploy via the shared SSM blue/green owner** — prod EC2 (`i-*`) and
   Lightsail Edge (`mi-*`) both call
   `ops/stage0/deploy_via_ssm_bluegreen.sh`. The managed-instance ID selects a
   thin `prod` or `edge` environment profile; it does not select a second
   deployment state machine.

   First run performs one migration cutover while legacy `tokenkey` continues
   serving: `.env` backup → derive the image repository → write
   `/var/lib/tokenkey/docker-compose.bluegreen.yml` → start
   `tokenkey-blue` with the requested image → wait Docker health and `/health`
   readiness → rewrite only the live Caddy `reverse_proxy` upstream to
   `tokenkey-blue:8080` and hot reload → atomically write
   `/var/lib/tokenkey/active-color=blue` and the cutover timestamp → observe the
   routed `/health` for 30 seconds → install the blue/green `tokenkey.service`
   → drain and stop legacy `tokenkey`.

   Every subsequent deploy alternates colors:
   `.env` backup → pull target tag → always force-recreate the inactive color
   (`tokenkey-blue` or `tokenkey-green`) → wait up to 300 seconds for Docker health
   (`exited`/`dead` fail immediately; three consecutive `unhealthy` checks fail
   early) and then `/health` readiness → rewrite only the live Caddy upstream
   to the target color and hot reload → atomically write `active-color` and the
   cutover timestamp → observe routed `/health` for 30 seconds →
   install/update the blue/green systemd unit → SIGUSR1/drain and stop the
   previous color. A pre-cutover failure captures bounded logs, removes the
   failed candidate, restores the pre-deploy `.env`, and leaves the active
   color untouched. Explicit exits and returned command failures share one
   `EXIT` cleanup path; an `ERR` trap is not the rollback owner.

   The externally meaningful state is only `pre_cutover` or `committed`.
   `commit_cutover` owns the Caddy backup/reload plus atomic `active-color` and
   timestamp write. A validation, reload, or persistence failure restores the
   old Caddy route and durable active state when both restorations succeed. If
   either restoration fails, it preserves the target route and both colors for
   explicit recovery instead of entering pre-cutover cleanup. After commit,
   routed `/health` must remain successful for 30 seconds before the old color
   is drained and stopped. A post-commit failure leaves both colors running for
   an explicit previous-tag rollback; it never flips back automatically.

   A target Caddy reload that cannot be confirmed removes `active-color` before
   returning failure. This intentionally makes the durable state incomplete
   instead of falsely claiming that the target is active. On host restart,
   `tokenkey.service` starts only the color jointly identified by Caddy and
   `active-color`. If they disagree or either value is unresolved, it starts
   both colors before Caddy so the persisted route stays serviceable. A colored
   Caddy route with no `active-color` is an explicit recovery state:
   `ensure_legacy_cutover` must block rather than treating it as first-run
   legacy migration, and every later deploy remains blocked until an operator
   makes route and durable active state agree.

   `ops/stage0/bluegreen-capacity-policy.env` is the single data owner of the
   Edge memory and disk thresholds. Only the deploy primitive and the read-only
   Edge release probe consume it. The canary picker consumes the probe verdict
   and facts; it does not parse or recompute the policy. Edge starts a candidate
   only when both hard admission rules pass:

   - `MemAvailable >= max(EDGE_MIN_MEM_AVAILABLE_BYTES, active app working set + EDGE_ACTIVE_APP_HEADROOM_BYTES)`;
   - root filesystem available space is at least
     `EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES`.

   Swap pressure, load, and recent OOM history are audit fields only. Protocol
   readiness is not reconstructed by deployment code: candidate `/health` is
   the single target-version decision owner.

   PostgreSQL, Redis, Caddy, `/var/lib/tokenkey/app`, and the Docker network
   remain the single shared data layer. EC2/Lightsail bootstrap owns the app
   bind-mount root as uid/gid 1000; Stage0 app containers set
   `SKIP_DATA_CHOWN=1`, and the entrypoint also auto-detects an already-owned
   bind mount, so startup never recursively walks large DLQ/blob trees. Other
   deployment shapes retain the compatibility recursive ownership repair. The
   generated blue/green compose only contains the two app services and points
   them at `tokenkey-postgres` / `tokenkey-redis`.
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
11. **Post-release live check** — after smoke and the SSOT display gate in the
   same `deploy` job, wait 5 minutes and score every product PR between the
   previously serving tag and this tag against prod logs
   (`scripts/release_post_check.py` +
   `ops/observability/run-post-release-check.sh`). The workflow plans once and
   passes `--plan-file` into the wrapper so check does not re-plan. It reuses
   the already approved prod Environment/OIDC so the clock starts after
   cutover, not after a second reviewer gate. Failure does not roll back the
   already switched color; it fails the workflow so the regression is visible.
   Empty product-commit ranges skip the wait.
12. **Feishu release rollout notification** — best-effort rollout card after
   smoke and post-release check (green or skip). Uses the pre-mutation runtime
   tag baseline for release notes.
13. **Job summary** — write app and QA Worker images, Worker source, rollout
   mode, host runtime mode, the SSM command id, and a one-line app rollback
   dispatch. Legacy rollback is explicitly degraded: it requires a fully
   verified live Worker, converges the current maintenance runner, disables
   boundary before app mutation, skips canary, and pauses DROP until a
   Phase 3 app is restored. No auto-rollback (would mask transient failures).
14. **Read-only QA infrastructure acceptance** — `operation=qa-infra-check`
   validates both IAM stack outputs, exact `QA_INFRA_OIDC_ROLE_ARN` binding,
   real deployment-role assumption, recognized raw-archive stack identity,
   and Bundle-era CloudFormation service-role binding. It contains no change
   set, SSM, host sync, canary, or image mutation.

Concurrency `group: deploy-stage0-prod`, `cancel-in-progress: false` is shared
by all operations. Workflow-level permission is `contents: read`; the `deploy`
job adds `id-token: write` and `packages: read`, `qa-infra-check` adds only
`id-token: write`, and `smoke-only` keeps the workflow default. No job
receives `contents: write`. The post-release live check is a later step in
`deploy`; it is read-only SSM plus git history and does not mutate host
colors.

### Edge release canary

The canary selector probes every deployable Edge and emits a complete JSON
audit. Missing or invalid memory, disk, or 30-minute completed-request facts
make that Edge ineligible. Among eligible Edges it sorts by:

1. lower completed requests in the prior 30 minutes;
2. greater `MemAvailable` headroom;
3. canonical Edge matrix order.

Native OAuth/Kiro pool size remains an audit and smoke-applicability field; it
does not affect eligibility or ordering. A fleet-wide empty pool is expected
and does not fail canary selection. Transport failure for one Edge rejects that
Edge; selection fails closed only when no eligible Edge remains.

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
- **No post-commit auto-rollback** — pre-cutover failures leave the old color
  untouched; an incomplete cutover commit restores the old Caddy route; after
  a committed cutover, re-dispatch the previous app tag so rollback goes
  through the same health/smoke path. QA Worker/host-runner rollback is a
  separate lifecycle governed only by
  `docs/approved/design-prod-qa-24h-s3-lifecycle.md` section 18.2.
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
  This is an app-image rollback; its QA control-plane behavior remains governed
  by the QA lifecycle SSOT linked above.

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
3. **Candidate failure preserves service**: explicit exit, liveness failure,
   or readiness failure removes the candidate and leaves Caddy,
   `active-color`, and the serving app unchanged.
4. **Cutover commit is recoverable**: Caddy reload or state-persistence failure
   restores the old route and durable active state when possible; if either
   restoration fails, the target route and both colors remain available for
   explicit recovery and the deployment reports failure. An unconfirmed target
   reload clears `active-color`; restart starts both colors and a colored route
   without durable active state blocks subsequent deployment.
5. **One steady-state app**: successful routed observation drains and stops the
   old color; a post-commit observation failure leaves it running for explicit
   rollback.
6. **Edge capacity is fail-closed**: insufficient/unknown required memory,
   disk, or traffic facts reject only that Edge before candidate start.
7. **Canary selection is deterministic**: eligible Edges sort by 30-minute
   traffic, memory headroom, and matrix order; OAuth/Kiro pool size cannot make
   selection fail.
8. **Migration admission has one entry**: prod and Edge call
   `scripts/checks/bluegreen-migration-safety.py`; workflows do not copy its
   tag/range preparation or SQL scan.
9. **Capacity policy has one owner**: only the deploy primitive and Edge probe
   consume `ops/stage0/bluegreen-capacity-policy.env`; the canary picker uses
   the probe verdict without reimplementing capacity policy.

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
- [x] Initial workflow status flipped to `shipped` (PR #53)
- [x] 2026-06-24 revision: prod deploy primitive changed from single-container
      restart to same-host blue/green, single data layer. PR #976 shipped the
      runtime change; PR #978 hardened the approved baseline, Caddy active
      upstream handling, SSM timeout, and migration-safety guard.
- [x] 2026-08-28 Edge reuse design approved: one shared primitive, two hard
      capacity gates, two-state cutover, and traffic-first canary selection.
- [ ] 2026-08-28 Edge reuse implementation merged and released.

### Adversarial gate verified

The fail-closed manifest precheck (Section 4 step 2 / Section 8 acceptance
#1) was confirmed by
[run 24872388875](https://github.com/youxuanxue/sub2api/actions/runs/24872388875):
dispatched with `tag=99.99.99`, exited at the GHCR manifest precheck
step **before** any AWS credential was configured or SSM command sent.
