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
writes a receipt. It does not export or delete data.

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
captured by the receipt while preserving all current unrelated settings.

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

The source query selects one deterministic page ordered by `(created_at, id)`.
It seals at most `max_rows`, records the first/last key and whether another cold
row exists, and does not refuse merely because the table has a larger cold
backlog. `run` requires `--cleanup-hold-receipt` and re-verifies the current
setting plus cleanup heartbeat immediately before the export.

The existing `tokenkey-stage0-backups` pgdump bucket expires this prefix with
the same short retention used for pgdump copies (seven days under the approved
stack configuration). This is canary staging, not long-term archive storage and
never evidence that production rows may be deleted. Merge does not authorize a
run: every execution still requires explicit approval plus the exact
confirmation string `tokenkey-prod-archive-export-only-v1`.
