# QA Operations

The approved target is defined by `docs/approved/design-prod-qa-24h-s3-lifecycle.md`.
Machine-readable policy lives in `ops/qa/policy.yaml`; repository readiness and observed live state are separate in `ops/qa/deploy_rollout.yaml`.

## Target owner

`tokenkey-qa-maintenance.timer` is the only target lifecycle owner. One run provisions future hourly children, archives and restore-verifies sealed hours, drops only exact archive-gated source partitions, then resumes exact-hour Blob/DLQ cleanup. `source_dropped_at` makes post-DROP cleanup bounded and idempotent.

`tokenkey-qa-boundary.timer` is transition-only. Before the append-only `single_owner_activate` receipt, its single timer path provisions future hours and preserves the existing 24-hour whole-partition cleanup. An expired hour without complete archive evidence is recorded as a terminal gap before DROP. Receipt existence permanently forces the boundary disabled/inactive, including sync rollback. There is no provision-only/manual cutover mode, export-orphan mode, or finalize mode.

User list, detail, and ZIP jobs use immutable Bundle S3 `spec.json` objects as their only registry. The prod database is consulted only for entitlement and API-key ownership; `qa_export_jobs` is not a Bundle fallback. Bundle infrastructure must pass `verify_qa_bundle_infra.sh`, and the canonical canary must traverse raw S3, SQS, Fargate, receipt, and manifest before the user path is considered ready.

The stale-cleanup timer, prod stale operator, export-orphan helper, `qa_exports_tmp` mount, and export activation marker are retired and are not packaged or synchronized.

## Bundle infrastructure bootstrap

This is the single operator entry for the IAM and GitHub configuration required before the first Bundle-aware prod deploy. It changes infrastructure and must be run only with separate production approval; repository readiness does not authorize it.

1. With approved administrator credentials, update `tokenkey-cicd-oidc` from `deploy/aws/cloudformation/cicd-oidc.yaml` using the existing OIDC setup command in `deploy/aws/README.md`. This creates the dedicated `QAInfraDeploymentRole` and restricted CloudFormation service role. Preserve the stack's existing `CreateOIDCProvider` parameter value.
2. Set GitHub Actions variable `QA_INFRA_OIDC_ROLE_ARN` to stack output `QAInfraDeploymentRoleArn`. Keep `CICD_OIDC_STACK_NAME=tokenkey-cicd-oidc` unless the stack uses a non-default name.
3. Set `QA_OPS_RECOVERY_PRINCIPAL_ARN` to the approved IAM user or role that may assume the raw-archive recovery role. It is required to create the QA stack; later deploys recover the durable value from the stack parameter.
4. Optional non-default stack names use `QA_RAW_ARCHIVE_STACK`. The deploy workflow resolves all remaining VPC, subnet, route-table, app-role, image, and browser-origin inputs from the target Stage0 stack and release tag.
5. Dispatching `deploy-stage0.yml` then deploys and verifies Bundle infrastructure before changing the prod app image. The verifier reads back the exact bucket CORS origin, AES256 encryption, job-surface lifecycle, running Fargate task/image, and queue/DLQ state; any missing or drifting prerequisite fails closed before the app mutation.

`QA_INFRA_OIDC_ROLE_ARN`, `QA_OPS_RECOVERY_PRINCIPAL_ARN`, `CICD_OIDC_STACK_NAME`, and `QA_RAW_ARCHIVE_STACK` are GitHub Actions variables, not secrets. No AWS long-lived credentials are added to GitHub.

App rollback does not imply QA control-plane rollback. The deploy workflow resolves the Bundle Worker image independently; legacy app rollback preserves the compatible Worker and Phase 3 host runners, skips the unsupported Bundle canary, and reports a degraded state. The complete resolver and recovery contract has one normative source: `docs/approved/design-prod-qa-24h-s3-lifecycle.md` section 18.2.

## State and checks

The repository may be `single_owner_ready` while observed live state remains `single_owner_not_activated`. Editing rollout metadata never proves deployment.

```bash
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 -m unittest ops.qa.test_resolve_qa_bundle_worker_image ops.stage0.test_deploy_stage0_workflow
python3 -m unittest ops.qa.test_qa_phase_ops
cd backend && go test -tags=unit ./internal/observability/qa/lifecycle ./cmd/server
```

Phase 2 archive/recovery closeout evidence remains in `docs/approved/design-qa-phase2-archive-closeout.md` and is explicitly historical/superseded for lifecycle ownership. Edge closeout evidence is retained only as history; it is not a deployable prod owner.
