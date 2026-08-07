# QA lifecycle ops (SSOT index)

Policy 数值不在此重复。Owner 表：

| Concern | Owner |
| --- | --- |
| Runtime policy (prod/edge targets) | [`policy.yaml`](policy.yaml) |
| Deploy env injection defaults (rollout gates) | [`deploy_rollout.yaml`](deploy_rollout.yaml) |
| Approved design + phase gates | [`docs/approved/design-prod-qa-24h-s3-lifecycle.md`](../../docs/approved/design-prod-qa-24h-s3-lifecycle.md) |
| Phase 2 archive closeout | [`docs/approved/design-qa-phase2-archive-closeout.md`](../../docs/approved/design-qa-phase2-archive-closeout.md) |
| Mechanical drift guard | [`scripts/checks/qa-lifecycle-ssot.py`](../../scripts/checks/qa-lifecycle-ssot.py) |

Generic usage/ops data-layer archive: [`ops/archive/README.md`](../archive/README.md).

`deploy_rollout.yaml` keeps `QA_ARCHIVE_ENABLED` deploy injection at `false` until Phase 2
closeout (`design-qa-phase2-archive-closeout.md`). Edge deploy always injects
`QA_CAPTURE_ENABLED=false` (see `policy.yaml` edge block).

Operator scripts: `prod_qa_maintenance.py`, `prod_qa_historical_closeout.py`,
`prod_qa_stale_cleanup.py`, `prod_qa_archive_closeout.py`, `prod_phase2_baseline.py`;
timer install: `sync-qa-maintenance-timer-via-ssm.sh`, `sync-qa-stale-cleanup-timer-via-ssm.sh`.
