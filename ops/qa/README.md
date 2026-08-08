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

`policy.yaml` also owns the archive runner UID/GID, real host/container scratch paths,
atomic host receipt path, one-window catch-up bound, and the stale-cleanup-owned export
temp path. The production cutover timestamp is live database control state, not policy.

`deploy_rollout.yaml` keeps `QA_ARCHIVE_ENABLED` deploy injection at `false` until the
approved Phase 2 production-integrity closeout is implemented and verified; approval of
the document alone is not activation. It records that the recently enabled live timer
must be stopped and reclosed through the single-runner rollout before the deploy default
can move to the policy target. Edge deploy always injects `QA_CAPTURE_ENABLED=false`.

Operator scripts: `prod_qa_maintenance.py`, `prod_qa_historical_closeout.py`,
`prod_qa_stale_cleanup.py`, `prod_qa_archive_closeout.py`, `prod_phase2_baseline.py`;
timer install: `sync-qa-maintenance-timer-via-ssm.sh`, `sync-qa-stale-cleanup-timer-via-ssm.sh`.
