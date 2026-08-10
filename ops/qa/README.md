# QA lifecycle ops (SSOT index)

Policy 数值不在此重复。Owner 表：

| Concern | Owner |
| --- | --- |
| Runtime policy (prod/edge targets) | [`policy.yaml`](policy.yaml) |
| Deploy env injection defaults (rollout gates) | [`deploy_rollout.yaml`](deploy_rollout.yaml) |
| Approved design + phase gates | [`docs/approved/design-prod-qa-24h-s3-lifecycle.md`](../../docs/approved/design-prod-qa-24h-s3-lifecycle.md) |
| Phase 2 archive closeout | [`docs/approved/design-qa-phase2-archive-closeout.md`](../../docs/approved/design-qa-phase2-archive-closeout.md) |
| Mechanical drift guard | [`scripts/checks/qa-lifecycle-ssot.py`](../../scripts/checks/qa-lifecycle-ssot.py) |
| Timer/operator host runner | [`deploy/aws/stage0/tokenkey-qa-maintenance.sh`](../../deploy/aws/stage0/tokenkey-qa-maintenance.sh) |
| Correlated Phase 2 health verdict | [`qa_phase2_health.py`](qa_phase2_health.py) |
| Age cleanup + export crash-orphan owner | [`tokenkey-qa-stale-cleanup.sh`](../../deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh) |
| Direct workstation recovery | [`backend/cmd/qa-archive`](../../backend/cmd/qa-archive) |
| Break-glass retirement evidence gate | [`qa_archive_recovery_gate.py`](qa_archive_recovery_gate.py) |

Generic usage/ops data-layer archive: [`ops/archive/README.md`](../archive/README.md).

`policy.yaml` also owns the archive runner UID/GID, real host/container scratch paths,
atomic host receipt path, one-window catch-up bound, and the stale-cleanup-owned export
temp host/container paths. The production cutover timestamp is live database control state,
not policy.

`deploy_rollout.yaml` keeps `QA_ARCHIVE_ENABLED` deploy injection at `false` until the
approved Phase 2 production-integrity closeout is implemented and verified; approval of
the document alone is not activation. It records that the recently enabled live timer
must be stopped and reclosed through the single-runner rollout before the deploy default
can move to the policy target. Edge deploy always injects `QA_CAPTURE_ENABLED=false`.

Operator scripts: `prod_qa_maintenance.py`, `prod_qa_historical_closeout.py`,
`prod_qa_stale_cleanup.py`, `prod_qa_archive_closeout.py`, `prod_phase2_baseline.py`;
timer install: `sync-qa-maintenance-timer-via-ssm.sh`, `sync-qa-stale-cleanup-timer-via-ssm.sh`.
Maintenance timer and operator execution both enter through the installed host runner; the
operator wrappers do not own a second Docker execution contract. `qa_phase2_health.py`
evaluates a structured snapshot and fails closed unless systemd, host receipt, DB heartbeat,
and archive control facts all describe the same fresh scheduled run.

`tokenkey-qa-stale-cleanup.sh --plan` always inventories the effective export temp bind,
including the default `/app/data/qa_exports_tmp` to
`/var/lib/tokenkey/app/qa_exports_tmp` mapping, and emits basename/size/mtime facts plus an
exact canonical plan hash without deleting files. The first export-orphan deletion uses
`prod_qa_stale_cleanup.py apply-export-orphans` with the plan's separate confirmation and
creates `/var/lib/tokenkey/qa-export-orphan-cleanup-activated.json`; until that marker exists,
scheduled stale cleanup continues age retention but only reports export candidates. This
path does not depend on archive completeness, cutover, or maintenance timer health, and it
never deletes `qa_export_jobs` rows.

`qa-archive inspect|verify|restore --workstation` assumes the dedicated recovery role and
reads the raw S3 window without loading app config or opening PostgreSQL. The three commands
must share an operator-generated `--recovery-run-id`; each receipt binds that run to the
window, bucket, role and command. Workstation restore additionally requires an explicit
`--restore-root`, a new direct child `--output`, and the window-bound privacy confirmation.
The root/output directories are mode 0700 and restored files are mode 0600.

`qa_archive_recovery_gate.py plan-retirement` treats synthetic evidence as shape validation
only. Production scope also requires exact expected window/bucket/role arguments and a
separate unexpired human high-risk approval JSON whose `evidence_sha256` binds the reviewed
receipt bundle. Production receipts must be no older than 24 hours and the approval must
postdate the final receipt. The gate emits a planned transition but never removes
`ops/prod/fetch-qa-dump.sh`. Actual IAM apply, independent production recovery evidence,
and script retirement remain approval-gated. Gateway and maintenance still share the EC2
instance role, so the current bucket policy is not process-level isolation.
