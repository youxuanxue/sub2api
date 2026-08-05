---
title: Stage0 Deploy Smoke-Only Mode
status: approved
approved_by: user in Pi session 019fcf1f-e51f-75d9-b66c-e3ff19dde563
approved_at: 2026-08-05
created: 2026-08-05
owners: [tk-platform]
scope: ".github/workflows/deploy-stage0.yml + ops/stage0/test_deploy_stage0_workflow.py"
related_incident_runs: ["30959013681", "30959390802", "30959711877"]
---

# Stage0 Deploy Smoke-Only Mode

## 1. Background

Prod is serving `ghcr.io/youxuanxue/sub2api:1.8.133` on the blue color, but three
`deploy-stage0.yml` runs stopped in the SSM deploy step before the canonical
post-deploy smoke steps. Re-dispatching a normal deploy solely to obtain smoke
evidence would recreate the inactive color and repeat the slow-start risk.

The existing workflow therefore needs a non-mutating mode that reuses the same
`prod` GitHub Environment secrets and canonical smoke runners without invoking
AWS, SSM, Docker, Caddy, image pull, container recreation, or host-state writes.

## 2. Decision

Extend `.github/workflows/deploy-stage0.yml` with a required choice input:

```yaml
operation:
  type: choice
  options: [deploy, smoke-only]
  default: deploy
```

Keep `tag` required in both modes. In `smoke-only`, the tag is an audit label for
the release being accepted; the mode does not claim to prove the live container
image. Live-image evidence remains owned by `assert-live-host-state` or an
operator-provided host-state report.

Use two jobs in the same workflow rather than adding conditions to every step in
the current job:

- `deploy`: runs only for `operation=deploy`; retains current behavior, `prod`
  Environment binding, concurrency, and AWS OIDC/package permissions.
- `smoke-only`: runs only for `operation=smoke-only`; binds the same `prod`
  Environment but grants only `contents: read`.

Workflow-level permissions become `contents: read`. The deploy job explicitly
adds `id-token: write` and `packages: read`; the smoke-only job does not receive
either capability.

## 3. Smoke-only data flow

1. Validate `tag` with `ops/stage0/validate-deploy-tag.sh` for a stable audit
   label.
2. Validate `TK_SMOKE_API_KEY` and `TK_FULLTEST_KEY` before making authenticated
   requests.
3. Set both `TOKENKEY_BASE_URL` and `TK_FULLTEST_BASE_URL` from prod
   Environment variable `PROD_API_URL`, with canonical fallback
   `https://api.tokenkey.dev`.
4. Run the existing full gateway runner:

   ```bash
   bash ops/stage0/post_deploy_smoke.sh
   ```

5. Run the existing SSOT display closeout gate:

   ```bash
   bash ops/observability/endpoint-compat-audit.sh \
     --ssot-model-matrix --gate --deploy-canary --deploy-closeout
   ```

6. Write a summary that identifies `operation=smoke-only`, the audit tag, API
   URL, and both gate outcomes. It must not say that this run deployed or
   changed the live image.

The existing `deploy` job continues to run its current post-deploy smoke and
SSOT gate after a successful image switch. This change does not weaken normal
deploy acceptance.

## 4. Failure handling

- Missing smoke secrets: fail before authenticated probes.
- Public or authenticated shape regression: fail the smoke-only job.
- Runtime capacity responses already classified as soft-degrade by
  `ops/stage0/smoke_lib.sh`: preserve existing canonical semantics; do not add a
  second classifier in workflow YAML.
- Any failure stops at reporting. Smoke-only never performs rollback or host
  mutation.
- A failed smoke-only run does not prove a release regression by itself; the
  operator must classify schema/control-plane failure versus account-pool or
  upstream runtime pressure before a rollback decision.

## 5. Security and production safety

The smoke-only job must not contain or call:

- `aws-actions/configure-aws-credentials`
- `aws` or SSM commands
- `deploy_via_ssm_bluegreen.sh`
- Docker or Compose commands
- Caddy reload or active-color writes
- pricing/runtime sync, account apply, database writes, or Redis writes
- Feishu release notification (which semantically means deploy + smoke passed)

The existing workflow concurrency group remains shared by both jobs so a
smoke-only run cannot overlap a prod deploy run under the same workflow.

## 6. Tests and acceptance

Add a workflow contract test at
`ops/stage0/test_deploy_stage0_workflow.py`. It must behaviorally parse/extract
the two job blocks and assert:

1. `operation` exposes exactly `deploy` and `smoke-only`, defaulting to `deploy`.
2. The deploy job retains OIDC/package permissions and the SSM deploy primitive.
3. The smoke-only job has only read-only repository permission and contains no
   deploy/AWS/host-mutation commands.
4. Both modes invoke the canonical full gateway smoke and SSOT display gate.
5. Smoke-only uses the `prod` Environment and canonical prod API fallback.
6. The smoke-only summary cannot describe the action as a deployment.

After merge, operational acceptance is a separately approved dispatch:

```bash
gh workflow run deploy-stage0.yml \
  -f operation=smoke-only \
  -f tag=1.8.133
```

The run is accepted only when logs contain `tk_post_deploy_smoke: OK`, the SSOT
display closeout gate succeeds, and the job conclusion is `success`. No dispatch
is part of the implementation PR.

## 7. Explicitly out of scope

- No change to blue/green startup, health timeout, `force-recreate`, entrypoint
  chown, or DLQ retention. Those are P1.
- No release/tag/image build and no edge rollout. Those are P2.
- No claim that smoke-only repairs the three historical failed workflow runs.
- No automatic dispatch after merge.
