---
title: Cloud-Agent-Driven Tag-and-Deploy Workflow
status: pending
approved_by: pending
approved_at: pending
created: 2026-04-23
owners: [tk-platform]
related_prs: []
scope: "GHA workflow + IAM scope expansion to let `gh workflow run deploy-stage0.yml` execute the documented生产升级 SOP via OIDC + SSM"
---

# Cloud-Agent-Driven Tag-and-Deploy Workflow

## 1. Why this exists

Today the loop "tag → release.yml builds image → operator SSH-equivalent SSM
command from workstation → verify health" requires an **operator-side step that
no automation can take**. Cloud agents (`bash scripts/release-tag.sh vX.Y.Z`)
can complete the first half but stall at the deploy step because the
`AWS_OIDC_ROLE_ARN` trust policy only accepts the GitHub Actions OIDC issuer —
not a cloud agent VM. The result is that every release is gated on the human
running a long `aws ssm send-command` block from `deploy/aws/README.md`
§生产升级 SOP, even though the command is mechanical and never varies between
releases.

This proposal adds **one** new workflow `.github/workflows/deploy-stage0.yml`
that wraps the exact documented SOP. After it ships, a cloud agent's full
release loop becomes:

```
bash scripts/release-tag.sh vX.Y.Z              # tag + push (existing)
gh workflow run release.yml --ref vX.Y.Z        # if release.yml didn't auto-fire
gh workflow run deploy-stage0.yml \              # NEW
  -f environment=test -f tag=X.Y.Z
gh run watch ...                                 # health-check rolled into the workflow
gh workflow run deploy-stage0.yml \              # after test verified
  -f environment=prod -f tag=X.Y.Z
```

No human step is required, except optionally a manual GitHub Environment
"prod" reviewer-approval gate (Section 7).

## 2. Why this is high-risk and needs explicit approval

This is a **prod-touching automation** per `product-dev.mdc` §高风险 — it
introduces:

- **不可逆状态变更触发器**: the workflow rewrites `/var/lib/tokenkey/.env` and
  restarts the production `tokenkey` container. A bug in the workflow can
  cause prod outage.
- **安全边界扩张**: the existing OIDC role (Section 3) is locked to one prod
  EC2 instance + the `tokenkey-prod-stage0` stack pattern + the `main` branch
  subject. Supporting test deploys requires expanding instance ARN and stack
  pattern; supporting tag-driven dispatch requires loosening the branch
  subject.
- **核心体验高爆炸半径**: a wrong tag, a wrong image arch (`simple_release=true`
  amd64-only on Graviton ARM), or an unhealthy container after restart all
  surface as immediate API outage on `api.tokenkey.dev`.

Because of this, the workflow is **dispatch-only** (no schedule, no PR auto
trigger), enforces a strict input grammar, refuses arch-mismatched images via
a manifest precheck, requires the GHCR multi-arch manifest to exist before
mutating `.env`, and writes a rollback snapshot every time. The operator
remains the human-in-the-loop via the GitHub Environment review gate; the
workflow only **automates the keystrokes** that the operator already runs by
hand.

## 3. Scope of IAM trust expansion

`deploy/aws/cloudformation/cicd-oidc.yaml` currently scopes the role to:

- **Trust subject**: `repo:youxuanxue/sub2api:ref:refs/heads/main`
- **Resource — instance**: single `TargetInstanceId` parameter, default
  `i-04a8afd18c997b8ac` (prod)
- **Resource — stack**: `arn:aws:cloudformation:…:stack/tokenkey-prod-stage0/*`
- **Resource — document**: `arn:aws:ssm:…::document/AWS-RunShellScript`
- **Action**: `ssm:SendCommand`, `ssm:GetCommandInvocation`,
  `ssm:ListCommandInvocations`, `ssm:ListCommands`, `cloudformation:DescribeStacks`

The expansion required by this proposal:

- **Trust subject**: add `repo:youxuanxue/sub2api:environment:prod` and
  `repo:youxuanxue/sub2api:environment:test` so dispatch from any branch /
  tag / commit can assume the role **only when** the workflow is
  bound to one of those Environments. This is the standard pattern for
  GitHub-Environment-gated AWS access and is **stricter** than the current
  branch-only subject because the Environment can require human review.
  The `main` branch subject is preserved for backward compatibility with
  `error-clustering-daily.yml` and `prod-log-dump.yml`.
- **Instance resource**: change from a scalar parameter to a
  `CommaDelimitedList`, default `i-04a8afd18c997b8ac` (prod) +
  the test stack's `InstanceId` (resolved per environment, see Section 5).
  The role grants `ssm:SendCommand` to either; the workflow choice of which
  one targets is gated by `inputs.environment`, not by the role.
- **Stack resource**: extend the `cloudformation:DescribeStacks` ARN list
  to include `tokenkey-test-stage0/*`.
- **Action**: unchanged (still `ssm:SendCommand` against
  `AWS-RunShellScript` only — no `ec2:`, no `iam:`, no `s3:`).

The role name (`tokenkey-gha-${AWS::Region}-error-clustering`) is kept for
backward compatibility — renaming it would force every consumer workflow to
update `vars.AWS_OIDC_ROLE_ARN` simultaneously. A Description update calls
out the broader scope.

## 4. What the new workflow does

`.github/workflows/deploy-stage0.yml` — `workflow_dispatch` only.

Inputs:

- `environment` (required, choice: `test|prod`) — selects the stack name and
  binds the job to the matching GitHub Environment.
- `tag` (required, string) — the released image tag (without the leading
  `v`), e.g. `1.6.0`. Must match `^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+|-beta\.[0-9]+)?$`.
- `simple_release_override` (optional bool, default `false`) — if `false`
  (default) the workflow refuses to deploy a tag whose `:latest` manifest is
  single-arch. Operators who knowingly want to ship an amd64-only image to
  an amd64-only host can set this to `true`. This guard exists because the
  v1.3.0 / v1.4.0 incidents were both caused by `simple_release=true`
  overwriting `:latest` with an amd64-only manifest that then crashed
  Graviton hosts in `exec format error`. Default-deny here is the
  mechanical complement to `release.yml`'s `simple_release=false` default.

Steps (each is a thin wrapper around the documented SOP, no custom logic):

1. **Validate inputs** — strict regex on `tag`, choice enforcement on
   `environment`.
2. **Configure AWS credentials via OIDC** —
   `aws-actions/configure-aws-credentials@v4`, role from
   `vars.AWS_OIDC_ROLE_ARN`.
3. **Resolve target instance** — `aws cloudformation describe-stacks` on the
   environment-specific stack (`tokenkey-${env}-stage0`), read `InstanceId`
   output. Fail loudly if the stack does not exist in this account.
4. **Verify GHCR multi-arch manifest** — pull
   `https://ghcr.io/v2/${repo}/manifests/${tag}` with the Actions token,
   require `application/vnd.docker.distribution.manifest.list.v2+json` (or
   OCI index) **AND** at least one `linux/amd64` + one `linux/arm64`
   descriptor. Skip when `simple_release_override=true`. Fail closed
   otherwise — this is the §9.1 trap rebuilt as a hard precheck.
5. **Run deploy on EC2 via SSM** — `aws ssm send-command` of the same
   commands documented in `deploy/aws/README.md` § 生产升级 SOP, verbatim:
     1. backup `.env` → `.env.before-${tag}`
     2. `sed` the image tag in `.env`
     3. `docker compose pull tokenkey`
     4. `docker compose up -d --no-deps tokenkey`
     5. health-poll loop (12 × 5 s)
     6. `docker compose ps`
     7. `docker logs tokenkey --since 2m | tail -20`
6. **External health-check** — `curl https://${api_domain}/health`,
   require HTTP 200 and total time < 5 s. The api domain comes from the
   matching CloudFormation stack output (`ApiUrl`).
7. **Job summary** — write the deployed tag, the SSM command id, and the
   first/last few lines of container logs to `$GITHUB_STEP_SUMMARY`.
8. **(automatic on failure)** — the workflow does **not** auto-rollback;
   manual rollback (also documented in `deploy/aws/README.md`) is one
   `gh workflow run deploy-stage0.yml -f environment=… -f tag=<previous>`
   away. Auto-rollback would silently mask transient failures and is
   explicitly out of scope.

Concurrency: `group: deploy-stage0-${{ inputs.environment }}`,
`cancel-in-progress: false` — two operators racing the same environment
queue safely; cross-environment deploys are independent.

Permissions: `contents: read`, `id-token: write`, `packages: read` (for the
GHCR manifest precheck). No `contents: write` — the workflow does not
mutate the repository.

## 5. Required pre-deploy operator setup

After this PR merges, the operator must (one-time):

1. **Update the IAM stack** with the new template:
   ```bash
   aws cloudformation deploy --region us-east-1 \
     --stack-name tokenkey-cicd-oidc \
     --template-file deploy/aws/cloudformation/cicd-oidc.yaml \
     --capabilities CAPABILITY_NAMED_IAM \
     --parameter-overrides \
       TargetInstanceIds="i-04a8afd18c997b8ac,<test-instance-id>" \
       AllowedSubjects="repo:youxuanxue/sub2api:ref:refs/heads/main,repo:youxuanxue/sub2api:environment:prod,repo:youxuanxue/sub2api:environment:test"
   ```

2. **Create GitHub Environments** (`prod` and `test`) in repo Settings →
   Environments. For `prod`, enable **Required reviewers** (yourself) and
   **Wait timer** (e.g. 60 s) so a manual approval click is required before
   the workflow proceeds. For `test`, no protection rules are needed.

3. **(Optional) Add repo variable** `TEST_STACK_NAME=tokenkey-test-stage0`
   if the default doesn't match (the default is the literal string
   `tokenkey-test-stage0`).

If step 2 is skipped, the workflow still runs but without the reviewer
gate — the OIDC trust still requires the Environment subject to match,
which means without the Environment existing the role assumption fails
fast. So the order is: 1 → 2 → first deploy.

## 6. What this proposal does NOT do

To stay scoped to "automate the existing manual SOP" (Jobs minimal API
surface):

- **No new infrastructure**: no Lambda, no Step Function, no SSM Document
  authoring. Reuses `AWS-RunShellScript` like every other workflow in the
  repo.
- **No CFN drift management**: the workflow does not touch the stage0 stack
  parameter `ImageTag`. Drift between CFN parameter and runtime
  `TOKENKEY_IMAGE` remains an accepted trade-off (`deploy/aws/README.md`
  §升级 / 发版).
- **No DB migrations / schema bumps**: the workflow only restarts the
  `tokenkey` container; PostgreSQL / Redis / Caddy are untouched. Schema
  migrations remain a separate concern handled by the application's startup
  hooks.
- **No multi-region**: the role is scoped to the single `AWS::Region` it's
  deployed in (`us-east-1`). Operators wanting to deploy to a second region
  must deploy a second OIDC stack there.
- **No automatic test → prod promotion**: the operator (or the cloud agent
  acting on operator instructions) must explicitly dispatch the prod deploy
  after verifying test. This is intentional — automation should not collapse
  the human review window.

## 7. Mechanical gates this PR adds

Per `product-dev.mdc` §"Hard Constraint Wiring" / `agent-contract-enforcement.mdc`
§"no soft rule without a check":

- **`scripts/preflight.sh`** — already wired (this doc lives in
  `docs/approved/`, so dev-rules `check_approved_docs.py` will assert the
  R1-R4 invariants stay valid).
- **`workflow_dispatch` input validation** — strict regex enforced in the
  first job step; the workflow refuses to run on malformed `tag`.
- **GHCR multi-arch manifest precheck** — fail-closed Step 4 above. This
  is the §9.1 mechanical complement.
- **OIDC trust binding to GitHub Environment** — the role assumption
  literally cannot succeed without the Environment subject, which means
  protection rules on `prod` (Required reviewers / Wait timer) are
  load-bearing on the AWS side, not just GitHub UI cosmetic.

## 8. Migration / rollback of this PR itself

If after merge this workflow turns out to misbehave:

- **Disable**: in repo Settings → Actions → Workflows → "Stage0 Deploy" →
  Disable workflow. Operators fall back to the manual SOP in
  `deploy/aws/README.md` §生产升级 SOP — that path is unchanged and
  remains supported.
- **Revert IAM**: re-deploy `cicd-oidc.yaml` with the original parameters
  (`TargetInstanceIds=i-04a8afd18c997b8ac`,
  `AllowedSubjects=repo:youxuanxue/sub2api:ref:refs/heads/main`). The role
  ARN does not change, so `error-clustering-daily.yml` / `prod-log-dump.yml`
  are unaffected.
- **No data migration**: nothing in this PR writes durable state, so revert
  is a config rollback only.

## 9. Acceptance criteria

Once merged + operator setup complete:

1. `gh workflow run deploy-stage0.yml -f environment=test -f tag=1.6.0`
   completes successfully and `curl https://test-api.tokenkey.dev/health`
   returns HTTP 200.
2. `gh workflow run deploy-stage0.yml -f environment=prod -f tag=1.6.0`
   prompts for the configured reviewer in the prod Environment, then on
   approval completes and `https://api.tokenkey.dev/health` returns 200.
3. Dispatching with `tag=99.99.99` (manifest doesn't exist on GHCR) fails
   at Step 4 (manifest precheck) **before** any SSM command is sent.
4. Dispatching with `tag=` from an amd64-only `simple_release=true` build
   fails at Step 4 unless `simple_release_override=true` is also passed.
5. `error-clustering-daily.yml` / `prod-log-dump.yml` continue to function
   unchanged (regression check on the trust expansion).

## 10. Status

- [ ] Proposal — awaiting human approval (this PR)
- [ ] IAM stack redeployed with broader trust + multi-instance ARN list
- [ ] GitHub Environments `prod` (with reviewer) + `test` created
- [ ] First successful test-stack deploy via `gh workflow run`
- [ ] First successful prod-stack deploy via `gh workflow run`
- [ ] Status flipped to `shipped` with the merge PR + first-deploy commit
      listed in `related_prs` / `related_commits`
