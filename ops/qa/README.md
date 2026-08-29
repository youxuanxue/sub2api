# QA Operations

The approved target is defined by `docs/approved/design-prod-qa-24h-s3-lifecycle.md`.
Machine-readable policy lives in `ops/qa/policy.yaml`; repository readiness and observed live state are separate in `ops/qa/deploy_rollout.yaml`.

## Target owner

`tokenkey-qa-maintenance.timer` is the only target lifecycle owner. One run provisions future hourly children, archives and restore-verifies sealed hours, drops only exact archive-gated source partitions, then resumes exact-hour Blob/DLQ cleanup. `source_dropped_at` makes post-DROP cleanup bounded and idempotent.

`tokenkey-qa-boundary.timer` is transition-only. Before the append-only `single_owner_activate` receipt, its single timer path provisions future hours and preserves the existing 24-hour whole-partition cleanup. An expired hour without complete archive evidence is recorded as a terminal gap before DROP. Receipt existence permanently forces the boundary disabled/inactive, including sync rollback. There is no provision-only/manual cutover mode, export-orphan mode, or finalize mode.

User list, detail, and ZIP jobs use immutable Bundle S3 `spec.json` objects as their only registry. The prod database is consulted only for entitlement and API-key ownership; `qa_export_jobs` is not a Bundle fallback. Bundle infrastructure must pass `verify_qa_bundle_infra.sh`. The canonical canary still traverses raw S3, SQS, Fargate, receipt, and manifest whenever the Worker or publisher surface changed, or surface classification fails closed. Gateway-only deploys reuse a verified live Worker and skip a repeat canary.

The stale-cleanup timer, prod stale operator, export-orphan helper, `qa_exports_tmp` mount, and export activation marker are retired and are not packaged or synchronized.

## Bundle infrastructure bootstrap

This is the single operator entry for the IAM and GitHub configuration required before the first Bundle-aware prod deploy. It changes infrastructure and must be run only with separate production approval; repository readiness does not authorize it.

1. With approved administrator credentials, update `tokenkey-cicd-oidc` from `deploy/aws/cloudformation/cicd-oidc.yaml` using the existing OIDC setup command in `deploy/aws/README.md`. This creates the dedicated `QAInfraDeploymentRole` and restricted CloudFormation service role. Preserve the stack's existing `CreateOIDCProvider` parameter value.
2. Set GitHub Actions variable `QA_INFRA_OIDC_ROLE_ARN` to stack output `QAInfraDeploymentRoleArn`. Keep `CICD_OIDC_STACK_NAME=tokenkey-cicd-oidc` unless the stack uses a non-default name.
3. Set `QA_OPS_RECOVERY_PRINCIPAL_ARN` to the approved IAM user or role that may assume the raw-archive recovery role. It is required to create the QA stack; later deploys recover the durable value from the stack parameter.
4. Optional non-default stack names use `QA_RAW_ARCHIVE_STACK`. The deploy workflow resolves all remaining VPC, subnet, route-table, app-role, image, and browser-origin inputs from the target Stage0 stack and release tag.
5. Dispatch `deploy-stage0.yml` with `operation=qa-infra-check` to verify both OIDC outputs, exact variable binding, real deployment-role assumption, QA stack read access, and the Bundle-era CloudFormation service-role binding without mutation. Only a stack exposing the canonical raw-archive outputs is recognized; a matching legacy pre-Bundle stack reports `legacy_bootstrap_ready`, while an unrelated/malformed stack or Bundle-era service-role drift fails.
6. Normal deploy then verifies Bundle infrastructure before changing the prod app image. Legacy rollback first runs the verifier in explicit discovery mode and accepts only its actual Worker image; post-update verification requires the resolved image explicitly.

`QA_INFRA_OIDC_ROLE_ARN`, `QA_OPS_RECOVERY_PRINCIPAL_ARN`, `CICD_OIDC_STACK_NAME`, and `QA_RAW_ARCHIVE_STACK` are GitHub Actions variables, not secrets. No AWS long-lived credentials are added to GitHub.

App rollback does not imply QA control-plane rollback. The target release tree explicitly declares `bundle_runtime_contract: phase3_v1`. Legacy app rollback preserves only a fully verified live Worker, converges the current Phase 3 maintenance runner, forces boundary disabled/inactive before the app switch, skips canary, pauses DROP, and reports a degraded state. The complete resolver and recovery contract has one normative source: `docs/approved/design-prod-qa-24h-s3-lifecycle.md` section 18.2.

## State and checks

The repository may be `single_owner_ready` while observed live state remains `single_owner_not_activated`. Editing rollout metadata never proves deployment.

The production handoff has one local SSM entry. `plan` is read-only and prints the
current immutable plan hash. `activate` requires that exact hash and confirmation;
the host runner still owns drain, lock, receipt, and boundary retirement.

```bash
bash ops/stage0/activate-qa-single-owner-via-ssm.sh plan <prod-instance-id>
bash ops/stage0/activate-qa-single-owner-via-ssm.sh activate <prod-instance-id> \
  --plan-hash=<sha256> \
  --confirm=tokenkey-prod-qa-single-owner-activate-v1:<sha256>
```

Plan failure is a hard stop. In particular, missing capture seals must age naturally
out of the approved 24-hour horizon; operators must not fabricate or backfill them.

```bash
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 -m unittest ops.qa.test_resolve_qa_bundle_worker_image ops.qa.test_qa_bundle_release_surface ops.stage0.test_deploy_stage0_workflow
python3 -m unittest ops.qa.test_qa_phase_ops
cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/server
```

Current lifecycle owner is `docs/approved/design-prod-qa-24h-s3-lifecycle.md`.
