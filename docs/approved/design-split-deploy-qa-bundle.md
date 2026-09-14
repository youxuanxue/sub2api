---
status: approved
approved_by: "feng (conversation 2026-09-14: confirmed independent normal releases, legacy-only rollback safety, and serialized QA acceptance)"
approved_at: 2026-09-14
scope: "split production workflows, legacy rollback safety, maintenance release acceptance, release plan/state owners, notifications and regression guards"
---

# Physical Separation of Prod Stage0 Gateway and QA Bundle Workflows

## 1. Why this exists

The 2026-09-10 component release revision (`docs/approved/prod-component-release.md`) introduced conditional component selection within a monolithic workflow (`deploy-stage0.yml`). However, production experience demonstrated that embedding heavy, out-of-band QA infrastructure deployment (CloudFormation ECS/SQS/S3 updates, taking 3.5m+) and end-to-end historical QA bundle canaries (taking 7m+) inside the primary gateway release loop introduces severe operational friction:

1. **Gateway Deployment Velocity**: Routine gateway releases (e.g. API compatibility, pricing, client fingerprint updates) require rapid, deterministic deployment (~5.5 minutes, bounded primarily by the mandatory 5-minute post-cutover observation gate).
2. **Blast Radius and Failure Domain Isolation**: The gateway is a producer writing raw QA events to SQS/S3. The QA Bundle Worker is an asynchronous downstream consumer. Under backward-compatible event payload schemas, gateway mutations must not be gated or blocked by worker infrastructure updates, CloudFormation stack synchronization, or heavy worker canary retries.
3. **IAM Privilege Segregation**: The gateway deployment role (`AWS_OIDC_ROLE_ARN`) requires only SSM RunShellScript invocation and CloudFormation DescribeStacks on Stage0. Normal releases do not require CloudFormation mutation or the separate `QA_INFRA_OIDC_ROLE_ARN` role. The legacy rollback exception below uses that role only to verify the retained Worker.

## 2. Architecture and Boundaries

### Primary Gateway Workflow (`.github/workflows/deploy-stage0.yml`)
- **Responsibility**: Gateway blue/green deployment, pre-deploy manifest and migration gates, post-deploy smoke, SSOT display gate, 5-minute post-cutover traffic/5xx stability observation, and request drain completion.
- **Removed from normal gateway releases**:
  - `QA_INFRA_OIDC_ROLE_ARN` validation and credentials assumption.
  - QA Bundle Worker discovery (`Discover existing QA Bundle Worker (read-only)`).
  - QA infrastructure deploy and verification (`Deploy QA Bundle infrastructure`, `Verify QA Bundle infrastructure`).
  - QA host maintenance synchronization (`Sync QA maintenance host runner`, `Sync QA boundary host runner`).
  - QA host systemd maintenance verification.
  - Post-deploy QA Bundle canary (`Post-deploy QA Bundle canary`).
  - Read-only `qa-infra-check` operation mode (moved to dedicated workflow).
- **Elapsed Time**: Approximately 5.5 minutes is an optimization target, not a hard bound. The five-minute observation starts after cutover; image preparation, smoke and request drain add time. Actual elapsed time requires production rollout evidence.

### Dedicated QA Bundle Workflow (`.github/workflows/deploy-qa-bundle.yml`)
- **Responsibility**: Deploy the requested-tag QA stack and maintenance runtime, verify systemd health and Bundle canary, then record the accepted component combination. The serving gateway remains unchanged.
- **Triggers**: `workflow_dispatch` with inputs:
  - `operation`: `deploy` (updates CFN and maintenance, verifies health/canary and records acceptance), `canary-only` (runs only the 7-minute canary on current infrastructure), `qa-infra-check` (read-only OIDC/IAM verification).
  - `tag`: Release image tag for the QA Bundle Worker.
- **Permissions**: Binds `prod` GitHub Environment; assumes `QA_INFRA_OIDC_ROLE_ARN` for stack mutation and `AWS_OIDC_ROLE_ARN` for canary SSM execution.

## 3. Invariants and Safety
1. **Producer/Consumer Compatibility**: Payload schemas emitted by the gateway to SQS/S3 remain append-only and backward compatible.
2. **Gateway Safety Preserved**: The 5-minute cutover observation window, blue/green request drain join, and fail-closed smoke gates in `deploy-stage0.yml` remain verbatim.
3. **Auditing**: Both workflows continue to publish Feishu notifications and post deployment summaries to GitHub step summaries.

## 4. Legacy rollback exception and acceptance ownership

`ops/stage0/prod_release_plan.py --operation classify` reads the target release tree.
Missing Bundle/independent runtime contracts select `legacy_rollback`; malformed or
unknown contracts fail before AWS credentials or host mutation. No operator-maintained
version cutoff or additional rollback switch is introduced.

Only legacy gateway rollback and the QA deploy/canary job share the `prod-qa-lifecycle`
GitHub job concurrency group with cancellation disabled. Normal gateway releases use
a different group and do not wait for QA infrastructure or canary. Existing workflow
concurrency continues to serialize gateway deploys with gateway replays.

Legacy rollback verifies the live Worker with the canonical discovery gate and reads
the independent maintenance pin. Missing evidence blocks rollback. It then pauses DROP
and disables Boundary before the blue/green mutation, preserving the Worker and the
maintenance image/config pin. It reports degraded QA and never clears the pause.

The QA workflow requires compatible requested-tag and observed serving-gateway contracts.
It pauses DROP before mutation, installs requested-tag host artifacts and maintenance
image with freshly verified Bundle coordinates, binds the runtime identity, and runs
systemd health plus canary. The canary uses the observed gateway tag so the recorded
publisher baseline describes the code actually tested. `canary-only` never installs
host artifacts, advances the verified component record, or clears the pause.

`prod_release_state.py record` takes the existing blue/green host lock before resolving
the active color, then the lifecycle lock. It rejects gateway/pin/host-artifact drift;
failed acceptance leaves the previous verified record and DROP pause intact. Successful
compatible acceptance records the combination and clears the pause without activating
single ownership. The existing Boundary installer respects the durable activation receipt.

The gateway only reads stack outputs to configure its Bundle producer; Worker capacity
and rollout status do not gate this coordinate lookup. QA notifications follow successful
acceptance and identify QA components explicitly. Workflow success is not evidence of
single-owner activation or a guaranteed wall-clock release duration.
