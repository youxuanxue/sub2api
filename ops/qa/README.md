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
| UTC-hour partition + hot-file/export-orphan owner | [`tokenkey-qa-boundary.sh`](../../deploy/aws/stage0/tokenkey-qa-boundary.sh) |
| Cutover drain-only legacy cleanup | [`tokenkey-qa-stale-cleanup.sh`](../../deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh) |
| Direct workstation recovery | [`backend/cmd/qa-archive`](../../backend/cmd/qa-archive) |
| Break-glass retirement evidence gate | [`qa_archive_recovery_gate.py`](qa_archive_recovery_gate.py) |
| Hash-bound expired-gap operator | [`prod_qa_archive_closeout.py`](prod_qa_archive_closeout.py) |

Generic usage/ops data-layer archive: [`ops/archive/README.md`](../archive/README.md).

`policy.yaml` also owns the archive runner UID/GID, real host/container scratch paths,
atomic host receipt path, one-window catch-up bound, and the boundary-owned export temp
host/container paths. The production cutover timestamp is live database control state,
not policy.

`deploy_rollout.yaml` keeps prod `QA_ARCHIVE_ENABLED` deploy injection aligned with
`policy.yaml` after the approved Phase 2 closeout: production deploy now injects archive
enabled by default, while edge deploy still injects `QA_CAPTURE_ENABLED=false`.

Operator scripts: `prod_qa_maintenance.py`, `prod_qa_historical_closeout.py`,
`prod_qa_stale_cleanup.py`, `prod_qa_archive_closeout.py`, `prod_phase2_baseline.py`;
timer install: `sync-qa-maintenance-timer-via-ssm.sh`, `sync-qa-boundary-timer-via-ssm.sh`.
Maintenance timer and operator execution both enter through the installed host runner; the
operator wrappers do not own a second Docker execution contract. `qa_phase2_health.py`
evaluates a structured snapshot and fails closed unless systemd, host receipt, DB heartbeat,
and archive control facts all describe the same fresh scheduled run.

Before finalize, the boundary timer stays disabled. After activate T0, extend the decaying
activation horizon without enabling cleanup or authorizing DROP:

```bash
sudo /usr/local/bin/tokenkey-qa-boundary.sh \
  --qa-cutover-provision-only \
  --confirm=tokenkey-prod-qa-cutover-provision-v1
```

This operator mode requires an activate receipt, rejects execution before T0 or after
finalize, takes the shared QAMA lock, and only provisions current-plus-72-hour children.
Finalize plan/apply remains a separate archive/export/catalog-gated operation. Its plan
includes every remaining empty legacy monthly child with exact schema, name, and bounds;
under the parent lock, apply rechecks that each is still attached and empty, then drops the
hash-bound monthly set and empty DEFAULT in one transaction. A nonempty monthly child,
unknown layout, missing/extra child, or bound drift fails before any DROP.

During no-move cutover drain, `tokenkey-qa-stale-cleanup.sh --plan` inventories the effective export temp bind,
including the default `/app/data/qa_exports_tmp` to
`/var/lib/tokenkey/app/qa_exports_tmp` mapping, and emits basename/size/mtime facts plus an
exact canonical plan hash without deleting files. The first export-orphan deletion uses
`prod_qa_stale_cleanup.py apply-export-orphans` with the plan's separate confirmation and
creates `/var/lib/tokenkey/qa-export-orphan-cleanup-activated.json`; until that marker exists,
scheduled stale cleanup continues age retention but only reports export candidates. This
path does not depend on archive completeness or maintenance timer health, and it never
deletes `qa_export_jobs` rows. After the durable finalize receipt, that timer is disabled;
the `*:00` boundary runner is the only partition, hot-file, and export-orphan cleanup owner.
The only operator plan envelope for this transitional path is
`prod_qa_cutover_drain_plan`, produced directly from the legacy runner:

```bash
python3 ops/qa/prod_qa_stale_cleanup.py plan \
  --instance-id i-0123456789abcdef0 \
  --output /tmp/prod-qa-cutover-drain-plan.json
```

Generic `ops/archive/data_layer_retention_activation.py` plans cannot drive QA cleanup.

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
postdate the final receipt. Break-glass prod QA dump tooling is retired after
production workstation recovery evidence passes the gate. Gateway and maintenance still share the EC2 instance role, so the
current bucket policy is not process-level isolation.

Expired zero-source archive gaps use one batch path in the same operator; there is no
arbitrary-window apply or second catch-up executable:

```bash
python3 ops/qa/prod_qa_archive_closeout.py gap-plan \
  --output /tmp/qa-gap-plan.json \
  --qa-archive-bin /path/to/target-tag/qa-archive \
  --recovery-run-id gap-YYYYMMDDTHHMMSSZ

python3 ops/qa/prod_qa_archive_closeout.py gap-apply \
  --plan /tmp/qa-gap-plan.json \
  --receipt-output /tmp/qa-gap-receipt.json \
  --confirm tokenkey-prod-qa-gap-decision-v1:<plan_hash> \
  --approved-by <approver>
```

`gap-plan` is read-only: the host emits database facts under `QAMA`, then the explicit
target-tag workstation binary assumes the dedicated recovery role and HEADs each candidate
`commit.json`. The SHA-256 binds DB anchors, exact windows, control/segment fingerprints,
bucket/role/recovery-run identity, source count, and S3 absence. `gap-apply` is a separate
high-risk approval gate; it revalidates all database facts under `QAMA`, writes only
`failed/source_unavailable_after_retention` through the existing shard owner, and inserts
the append-only approval receipt in the same transaction. It never reads or writes S3,
deletes hot data, moves/copies rows, or changes timers.

The host DB plan and apply command use bounded `gzip+base64` transport so a large historical
batch cannot be silently truncated by SSM. The operator and Go CLI reject transport or
decompressed-plan overflow before parsing; the reviewed SHA-256 still binds canonical
uncompressed JSON.
