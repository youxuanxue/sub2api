---
status: approved
approved_by: "feng (conversation 2026-09-13: approved Plan A to separate QA Bundle infrastructure/canary from deploy-stage0 into dedicated workflow)"
approved_at: 2026-09-13
scope: ".github/workflows/deploy-stage0.yml + .github/workflows/deploy-qa-bundle.yml + ops/stage0/test_deploy_stage0_workflow.py + ops/stage0/test_deploy_qa_bundle_workflow.py"
---

# Physical Separation of Prod Stage0 Gateway and QA Bundle Workflows

## 1. Why this exists

The 2026-09-10 component release revision (`docs/approved/prod-component-release.md`) introduced conditional component selection within a monolithic workflow (`deploy-stage0.yml`). However, production experience demonstrated that embedding heavy, out-of-band QA infrastructure deployment (CloudFormation ECS/SQS/S3 updates, taking 3.5m+) and end-to-end historical QA bundle canaries (taking 7m+) inside the primary gateway release loop introduces severe operational friction:

1. **Gateway Deployment Velocity**: Routine gateway releases (e.g. API compatibility, pricing, client fingerprint updates) require rapid, deterministic deployment (~5.5 minutes, bounded primarily by the mandatory 5-minute post-cutover observation gate).
2. **Blast Radius and Failure Domain Isolation**: The gateway is a producer writing raw QA events to SQS/S3. The QA Bundle Worker is an asynchronous downstream consumer. Under backward-compatible event payload schemas, gateway mutations must not be gated or blocked by worker infrastructure updates, CloudFormation stack synchronization, or heavy worker canary retries.
3. **IAM Privilege Segregation**: The gateway deployment role (`AWS_OIDC_ROLE_ARN`) requires only SSM RunShellScript invocation and CloudFormation DescribeStacks on Stage0. It does not require CloudFormation mutation or the separate `QA_INFRA_OIDC_ROLE_ARN` role.

## 2. Architecture and Boundaries

### Primary Gateway Workflow (`.github/workflows/deploy-stage0.yml`)
- **Responsibility**: Gateway blue/green deployment, pre-deploy manifest and migration gates, post-deploy smoke, SSOT display gate, 5-minute post-cutover traffic/5xx stability observation, and request drain completion.
- **Removed Steps**:
  - `QA_INFRA_OIDC_ROLE_ARN` validation and credentials assumption.
  - QA Bundle Worker discovery (`Discover existing QA Bundle Worker (read-only)`).
  - QA infrastructure deploy and verification (`Deploy QA Bundle infrastructure`, `Verify QA Bundle infrastructure`).
  - QA host maintenance synchronization (`Sync QA maintenance host runner`, `Sync QA boundary host runner`).
  - QA host systemd maintenance verification.
  - Post-deploy QA Bundle canary (`Post-deploy QA Bundle canary`).
  - Read-only `qa-infra-check` operation mode (moved to dedicated workflow).
- **Elapsed Time**: Approximately 5.5 minutes is an optimization target, not a hard bound. The five-minute observation starts after cutover; image preparation, smoke and request drain add time. Actual elapsed time requires production rollout evidence.

### Dedicated QA Bundle Workflow (`.github/workflows/deploy-qa-bundle.yml`)
- **Responsibility**: Standalone management of the QA Raw Archive / Bundle Worker CloudFormation stack and worker verification.
- **Triggers**: `workflow_dispatch` with inputs:
  - `operation`: `deploy` (updates CFN and executes canary), `canary-only` (runs only the 7-minute canary on current infrastructure), `qa-infra-check` (read-only OIDC/IAM verification).
  - `tag`: Release image tag for the QA Bundle Worker.
- **Permissions**: Binds `prod` GitHub Environment; assumes `QA_INFRA_OIDC_ROLE_ARN` for stack mutation and `AWS_OIDC_ROLE_ARN` for canary SSM execution.

## 3. Invariants and Safety
1. **Producer/Consumer Compatibility**: Payload schemas emitted by the gateway to SQS/S3 remain append-only and backward compatible.
2. **Gateway Safety Preserved**: The 5-minute cutover observation window, blue/green request drain join, and fail-closed smoke gates in `deploy-stage0.yml` remain verbatim.
3. **Auditing**: Both workflows continue to publish Feishu notifications and post deployment summaries to GitHub step summaries.
