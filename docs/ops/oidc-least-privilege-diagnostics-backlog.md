# Backlog: least-privilege OIDC role for ops-daily-diagnostics

> **Status: backlog / design note — NOT a shipped or approved baseline.**
> No code or IAM has changed for this. This note captures a security finding
> and a design so it can be executed deliberately (human-driven, with a live
> AWS IAM apply) when prioritized. It came out of the 2026-05 workflow god-eye
> review; that review's batches 1-5 shipped, this item was deferred because it
> is an architecture + live-prod-IAM change, not a workflow tweak.

## Finding (god-eye security lens, HIGH)

A single GitHub-OIDC IAM role — `tokenkey-gha-<region>-error-clustering`, exposed
to workflows as the repo variable `AWS_OIDC_ROLE_ARN` — is assumed by **five**
workflows: `deploy-stage0`, `deploy-edge-stage0`, `deploy-edge-lightsail-stage0`,
`ops-stage0-pg-dump-refresh`, and `ops-daily-diagnostics`. The role can run
`ssm:SendCommand` (AWS-RunShellScript = root shell) on prod and every Edge host,
plus CloudFormation change-set deploys, EIP rotation, and `iam:PassRole`.

The deploy workflows constrain this with a GitHub `environment:` binding
(`prod` / `edge-<id>`), which both narrows the OIDC `sub` claim and enables
required-reviewer gates. **`ops-daily-diagnostics` does not bind an
`environment:`** — its 02:00 UTC cron assumes the same full-fleet role under the
`repo:youxuanxue/sub2api:ref:refs/heads/main` subject, unattended, with no
reviewer gate. One compromise of that path (a bad commit to `main`, a malicious
dependency, a diagnostics-path bug) inherits root-shell over the whole fleet.

## Current state (precise anchors)

- Role + trust: `deploy/aws/cloudformation/cicd-oidc.yaml` — `ClusteringRole`
  (resource `ClusteringRole`, ~L111+). Trust `AllowedSubjects` default (~L19)
  includes `repo:youxuanxue/sub2api:ref:refs/heads/main` **and** the
  `environment:prod` / `environment:edge-*` subjects.
- Permissions (~L148+): `ssm:SendCommand` is resource-scoped to **specific
  instance ARNs** (prod + named edges) + the `AWS-RunShellScript` document +
  specific regions; plus `ssm:GetCommandInvocation/ListCommandInvocations`,
  `ssm:DescribeInstanceInformation`, `ec2:DescribeAddresses/AllocateAddress/
  ReleaseAddress` (EIP rotation), `cloudformation:DescribeStacks` +
  `CreateChangeSet/ExecuteChangeSet` (edge stacks), `sts:AssumeRole` (edge CFN
  exec roles), `iam:PassRole`. Lightsail addon
  (`cicd-oidc-lightsail-addon.yaml`) attaches `lightsail:*` + SSM activation/
  parameter actions to the **same** role.
- `AWS_OIDC_ROLE_ARN` is a **repo variable** (not a secret), set to the stack's
  `RoleArn` output (`deploy/aws/README.md`).
- Approved baseline governing this IAM design:
  `docs/approved/deploy-stage0-workflow.md` (approved_by: youxuanxue, PR #53),
  §3 "IAM Trust Expansion".

**Blast-radius nuance:** the `ssm:SendCommand` resource scoping means this is
"root shell on the *known* TokenKey fleet via the standard shell document," not
arbitrary AWS-account access. The diagnostics-specific gap is the missing
environment gate + no reviewer + unattended cron, not an unscoped role.

## Blocker: diagnostics is not read-only

`ops-daily-diagnostics` performs a **mutating install** on prod:
`ops/stage0/deploy-error-clustering-binary.sh` (invoked ~L403 of the workflow)
SSM-sends a shell command that `go build`s an `error_clustering` binary in a
transient Docker container on the host and `sudo install`s it to
`/usr/local/bin/`. So a "read-only diagnostics role" (the god-eye framing) is
**not directly achievable** — the mutating install must move out first, or the
role stays write-capable.

## Two paths

### Option A — full (diagnostics becomes read-only)
Move the `error_clustering` binary build/install out of the diagnostics path
into a deploy/setup workflow. Then `ops-daily-diagnostics` is genuinely
read-only, and gets a read-only role (`cloudformation:DescribeStacks` +
`ssm:GetCommandInvocation` + the describe/list reads; possibly no
`ssm:SendCommand` at all if log collection moves to a pull model). Most files
touched, most restructuring; fully realizes the god-eye goal.

### Option B — partial (narrowed diagnostics role) — recommended starting point
Keep the install in diagnostics, but give diagnostics a **new, narrowed role**:
- KEEP: `cloudformation:DescribeStacks` (prod stack), `ssm:SendCommand`
  (AWS-RunShellScript, instance-scoped), `ssm:GetCommandInvocation` /
  `ListCommandInvocations`, `ssm:GetParameter` (lightsail managed-instance id).
- DROP: `cloudformation:CreateChangeSet/ExecuteChangeSet/DeleteChangeSet`,
  `ec2:AllocateAddress/ReleaseAddress/CreateTags` (EIP rotation),
  `sts:AssumeRole` (edge CFN exec roles), `iam:PassRole`.
Bind diagnostics to a dedicated low-trust `environment:` (e.g. `ops-readonly`),
add that environment subject to `AllowedSubjects`, and consider removing
`ref:refs/heads/main` from the **deploy** role's trust (after which any
`push:main`-triggered step that assumes the role must bind its own environment).
Blast radius drops from "deploy + rotate + assume exec roles across the fleet"
to "run an SSM shell on already-known instances."

## Live apply order (critical) + rollback

1. **CFN apply first**: update the `tokenkey-cicd-oidc` stack so the new role
   exists in AWS *before* any workflow references it. (Local apply is not
   possible from CI checkouts; this is a human / authorized-path step on live
   prod IAM — a high-risk approval gate per CLAUDE.md §1.)
2. **Then** merge the `ops-daily-diagnostics` workflow change (new role ARN +
   `environment:` binding).
3. **Verify** the next 02:00 UTC cron (or a manual dispatch) assumes the new
   role and completes.
4. **Rollback**: point the workflow back at the existing `AWS_OIDC_ROLE_ARN`
   (keep the old role until the new one is proven).

## Guard gap to close on execution

`scripts/checks/lightsail-oidc-perm-coverage.py` (preflight: "Lightsail OIDC
perm coverage") only asserts the Lightsail action set, not the diagnostics role.
When A/B is executed, extend that check (or add a sibling) to cover the new
diagnostics role's expected actions so the split can't silently drift.

## Sibling backlog item: PAT → GitHub App (god-eye security lens, MEDIUM)

Three agent workflows (`pr-repair-agent`, `upstream-issue-watchdog`,
`upstream-merge-agent-daily`) share one long-lived PAT,
`UPSTREAM_MERGE_GH_TOKEN`, used as `GH_TOKEN` and embedded in the `origin` remote
URL. One PAT is the whole write blast radius for scan/fix/merge/repair. Prefer a
GitHub App with fine-grained, per-repo, short-TTL installation tokens minted per
run. This is org-level manual infra (create + install the App, store its key as
a secret), so like the OIDC split it is human-driven, not a pure-PR change.

## Provenance

- Source: 2026-05 workflow god-eye review (security + reliability lenses).
- Shipped sibling work: PRs #463 (P0 hardening), #465 (deploy tag-format gate),
  #467 (run-headless-agent composite, incl. the redactor fail-closed hardening),
  #469 (governance consolidation), #470 (deploy reliability residuals).
- This item: deferred from the automated batch flow by operator decision because
  it is an architecture + live-IAM change. Pick A or B, then update
  `docs/approved/deploy-stage0-workflow.md` through the approved-doc process.
