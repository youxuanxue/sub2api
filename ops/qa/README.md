# QA lifecycle ops (SSOT index)

Do not duplicate policy values in this file. Read owners below.

## Canonical owners

| Concern | Owner |
| --- | --- |
| Runtime policy (prod/edge targets) | [`policy.yaml`](policy.yaml) |
| Deploy env injection defaults (rollout gates) | [`deploy_rollout.yaml`](deploy_rollout.yaml) |
| Approved design + phase gates | [`docs/approved/design-prod-qa-24h-s3-lifecycle.md`](../../docs/approved/design-prod-qa-24h-s3-lifecycle.md) |
| Phase 2 archive closeout | [`docs/approved/design-qa-phase2-archive-closeout.md`](../../docs/approved/design-qa-phase2-archive-closeout.md) |
| Mechanical drift guard | [`scripts/checks/qa-lifecycle-ssot.py`](../../scripts/checks/qa-lifecycle-ssot.py) |

Generic **usage/ops** data-layer archive, retention inventory, and cleanup hold are owned by
[`ops/archive/README.md`](../archive/README.md) — not this directory.

## Deploy vs policy

`policy.yaml` states prod `archive.enabled: true` as the **target** runtime contract.
`deploy_rollout.yaml` keeps `QA_ARCHIVE_ENABLED` deploy injection at `false` until
Phase 2 closeout completes; operators enable archive env and the maintenance timer only
after the closeout checklist in `design-qa-phase2-archive-closeout.md`.

Edge deploy scripts always inject `QA_CAPTURE_ENABLED=false` and never wire QA archive env
(see `policy.yaml` edge block).

## Operator entry points (repo only)

| Intent | Tool |
| --- | --- |
| Phase 2 baseline probe (read-only) | `ops/qa/prod_phase2_baseline.py` |
| Hourly archive maintenance (guarded) | `ops/qa/prod_qa_maintenance.py` |
| Historical state closeout (guarded) | `ops/qa/prod_qa_historical_closeout.py` |
| Age retention apply (first cleanup, guarded) | `ops/qa/prod_qa_stale_cleanup.py` |
| Closeout inspect/verify/restore/repair | `ops/qa/prod_qa_archive_closeout.py` |
| Install maintenance units (timer default off) | `ops/stage0/sync-qa-maintenance-timer-via-ssm.sh` |
| Install stale-cleanup timer (default off) | `ops/stage0/sync-qa-stale-cleanup-timer-via-ssm.sh` |
| Raw archive bucket stack | `ops/qa/deploy_qa_raw_archive_cfn.sh` |
