# Data-layer archive rehearsal

This directory contains two deliberately separate archive surfaces. The
rehearsal CLI is local/non-production only: its SQLite path is the deterministic
baseline and `snapshot-postgres` accepts only a localhost Docker PostgreSQL with
the rehearsal sentinel. The production canary CLI is an explicit, export-only
operator command described below; it has no delete, schedule, workflow, or
deployment integration.

## Source contract

Prepare a local SQLite file with this table:

```sql
CREATE TABLE archive_rehearsal_records (
  dataset TEXT NOT NULL,
  record_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  PRIMARY KEY (dataset, record_id)
);
```

`dataset` is `usage`, `ops`, or `qa`; `created_at` is timezone-aware ISO 8601;
`payload_json` is valid finite JSON. The tool opens this database with SQLite
`mode=ro` and `query_only`. UTC normalization preserves source microseconds.

## Rehearsal

```bash
python3 ops/archive/data_layer_archive_rehearsal.py dry-run \
  --source /path/to/nonprod.sqlite --environment nonprod \
  --as-of 2026-07-20T00:00:00Z

python3 ops/archive/data_layer_archive_rehearsal.py seal \
  --source /path/to/nonprod.sqlite --environment nonprod \
  --as-of 2026-07-20T00:00:00Z --archive-root /path/to/archive-root

python3 ops/archive/data_layer_archive_rehearsal.py verify \
  --batch /path/to/archive-root/rehearsal-...

python3 ops/archive/data_layer_archive_rehearsal.py restore-random \
  --batch /path/to/archive-root/rehearsal-... \
  --target /path/to/fresh-restore.sqlite --seed 20260720
```

The defaults retain usage for 90 days, ops for 30 days, and QA for 2 days.
Every manifest keeps `deletion_authorized=false`; there is no deletion command.
The sealed source path and file identity prevent restore targets from pointing
back to the source through another path or hard link.
Production access is not available through the rehearsal CLI. The separate
production canary below does not loosen any of these source restrictions.

## PostgreSQL phase 3

The end-to-end command is deliberately narrow:

```bash
PGPASSWORD="$LOCAL_REHEARSAL_PASSWORD" \
python3 ops/archive/data_layer_archive_rehearsal.py snapshot-postgres \
  --source-dsn postgresql://tokenkey@127.0.0.1:5433/tokenkey_archive_rehearsal \
  --target-dsn postgresql://tokenkey@127.0.0.1:5433/tokenkey_archive_restore_20260720 \
  --archive-root /tmp/tokenkey-archive-rehearsal \
  --environment nonprod --as-of 2026-07-20T00:00:00Z --seed 20260720
```

The source is accepted only when all of these hold:

- URI host is `localhost`, `127.0.0.1`, or `::1`;
- database is exactly `tokenkey_archive_rehearsal`;
- `archive_rehearsal_sentinel` contains the label `tokenkey_archive_rehearsal`;
- only `usage_logs`, `ops_system_logs`, `ops_error_logs`, and `qa_records` are queried.

The target must be a separate database whose name starts with
`tokenkey_archive_restore_`. The command runs `dry-run -> seal -> verify ->
restore-random`, uses read-only source transactions with lock/statement
timeouts and a row cap, and reports elapsed time, source/candidate rows,
logical/artifact bytes, compression ratio, and restore verification. It never
deletes source or target data.

## Production export-only canary

Production archive work first requires an explicit cleanup hold. The controller
reads the current advanced settings through the admin API and cross-checks the
database heartbeat. `apply` preserves the complete settings document and changes
only `data_retention.cleanup_enabled`; it then proves the runtime cron reload and
writes a receipt. Repeating `apply` while a hold is already active is refused so
the receipt cannot lose the original enabled state. It does not export or delete data.

```bash
python3 ops/archive/data_layer_archive_cleanup_hold.py plan

python3 ops/archive/data_layer_archive_cleanup_hold.py apply \
  --receipt /path/to/cleanup-hold.json \
  --confirm tokenkey-prod-archive-cleanup-hold-v1

python3 ops/archive/data_layer_archive_cleanup_hold.py verify \
  --receipt /path/to/cleanup-hold.json
```

`release` is a separate production change and requires
`tokenkey-prod-archive-cleanup-release-v1`. It restores only the enabled state
captured by the receipt while preserving all current unrelated settings. Before
restoring, it revalidates that the same receipt's hold is still active and that
no cleanup has run since that hold began.

The offline plan validates the fixed 30-day waterline and hard limits without
calling AWS, Docker, PostgreSQL, or S3:

```bash
python3 ops/archive/data_layer_archive_prod_canary.py plan \
  --table ops_system_logs \
  --as-of 2026-07-21T03:00:00Z
```

The `run` command is a separately approved production operation. It resolves
only `tokenkey-prod-stage0` in `us-east-1`, verifies
`Project=tokenkey`/`Environment=prod`, exports through SSM from the local
`tokenkey-postgres` container in a read-only transaction, and accepts only
`ops_system_logs` or `ops_error_logs`. It uploads the artifact before the
manifest under `prod/pgdump/archive-canary/`, verifies S3 encryption and
checksums, then restores into an independent localhost database named
`tokenkey_archive_restore_*`.

The controller ships its deterministic Python bundle through an encrypted,
checksum-bound object under `prod/pgdump/archive-canary/control/`; SSM carries
only the bounded loader command. The source host verifies that bundle and the
live cleanup hold before opening the read-only source query.

The source query selects one deterministic page ordered by `(created_at, id)`.
It seals at most `max_rows`, records the first/last key and whether another cold
row exists, and does not refuse merely because the table has a larger cold
backlog. `run` requires `--cleanup-hold-receipt` and re-verifies the current
setting plus cleanup heartbeat on both the controller path and the source host
immediately before the export. Bigint source IDs use an order-preserving encoding
inside the artifact while manifest cursor keys retain the numeric `id`.

The existing `tokenkey-stage0-backups` pgdump bucket expires this prefix with
the same short retention used for pgdump copies (seven days under the approved
stack configuration). This is canary staging, not long-term archive storage and
never evidence that production rows may be deleted. Merge does not authorize a
run: every execution still requires explicit approval plus the exact
confirmation string `tokenkey-prod-archive-export-only-v1`.

## Production legacy cold batch export

After the canary proves the export-only path, legacy cold rows can be exported in
deterministic `(created_at, id)` pages without waiting for the partition-drop
calendar. Scope is strictly:

- `created_at < min(now - 30d, 2026-07-01T00:00:00Z)` (legacy attach bound)
- read-only source query with cursor continuation
- export-only: no delete, no partition drop

Staging uses a separate prefix `prod/pgdump/archive-export/` in the same
short-retention backup bucket (seven days). A local ledger records
`cursor_after` and `more_cold_rows_remaining` between batches.

Offline plan:

```bash
python3 ops/archive/data_layer_archive_prod_export.py plan \
  --table ops_system_logs
```

Initialize a continuation ledger once per table:

```bash
python3 ops/archive/data_layer_archive_prod_export.py init-ledger \
  --ledger .testing/user-stories/attachments/US-040-ops-system-logs-export-ledger.json \
  --table ops_system_logs
```

Export one batch (requires active cleanup hold receipt):

```bash
python3 ops/archive/data_layer_archive_prod_export.py run-batch \
  --ledger .testing/user-stories/attachments/US-040-ops-system-logs-export-ledger.json \
  --evidence-root /tmp/tokenkey-prod-export-evidence \
  --cleanup-hold-receipt .testing/user-stories/attachments/US-039-prod-cleanup-hold-20260721.json \
  --confirm tokenkey-prod-archive-export-batch-v1
```

Repeat `run-batch` until the ledger reports `more_cold_rows_remaining=false`.
Each batch still requires the exact confirmation string
`tokenkey-prod-archive-export-batch-v1` and an active cleanup hold.

## Long-term archive bucket and promote

Staging (`archive-export/` in the pgdump bucket) expires in **seven days**. Promote
copies committed export batches into the dedicated archive bucket (**90d Standard →
400d total retention**). Design baseline:
`docs/approved/design-prod-archive-bucket.md` (approved).

Deploy the archive stack once (same `AppInstanceRoleArn` pattern as backups):

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-stage0-archive \
  --template-file deploy/aws/cloudformation/stage0-archive.yaml \
  --parameter-overrides AppInstanceRoleArn=<prod InstanceRole ARN>
```

Promote one batch after export:

```bash
python3 ops/archive/data_layer_archive_promote_batch.py plan \
  --batch-id prod-export-20260722T112823.174855Z-8a928e2ea2c9

python3 ops/archive/data_layer_archive_promote_batch.py promote \
  --batch-id prod-export-20260722T112823.174855Z-8a928e2ea2c9 \
  --confirm tokenkey-prod-archive-promote-batch-v1
```

Promote every batch listed in an export ledger (idempotent):

```bash
python3 ops/archive/data_layer_archive_promote_batch.py promote-ledger \
  --export-ledger .testing/user-stories/attachments/US-040-ops-system-logs-export-ledger.json \
  --promote-ledger .testing/user-stories/attachments/US-040-ops-system-logs-promote-ledger.json \
  --confirm tokenkey-prod-archive-promote-batch-v1
```

Legacy partition drop remains blocked until export ledger complete **and** every
batch has a promote receipt in the promote ledger.
