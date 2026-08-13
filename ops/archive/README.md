# Data-layer archive rehearsal

SSOT：`pipeline_status.yaml`（repo 证据路径与 hot-layer retention 天数，与
`data_layer_archive_rehearsal.py` 机械对齐）。QA 生命周期见 [`ops/qa/README.md`](../qa/README.md)。

This directory contains two deliberately separate archive surfaces. The
rehearsal CLI is local/non-production only: its SQLite path is the deterministic
baseline and `snapshot-postgres` accepts only a localhost Docker PostgreSQL with
the rehearsal sentinel. The production canary CLI is an explicit, export-only
operator command described below; it has no delete, schedule, workflow, or
deployment integration.

Retention day defaults: `pipeline_status.yaml` (preflight:
`scripts/checks/data-layer-archive-ssot.py`).

`data_layer_retention_activation.py` owns production usage/ops only. It does not query,
plan, or activate QA retention; the QA lifecycle owner and operator entrypoints remain in
[`ops/qa/README.md`](../qa/README.md).

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

`dataset` is `usage` or `ops`; `created_at` is timezone-aware ISO 8601;
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

The defaults match `pipeline_status.yaml` hot-layer retention days. QA uses its dedicated lifecycle owner.
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
- only `usage_logs`, `ops_system_logs`, and `ops_error_logs` are queried.

The target must be a separate database whose name starts with
`tokenkey_archive_restore_`. The command runs `dry-run -> seal -> verify ->
restore-random`, uses read-only source transactions with lock/statement
timeouts and a row cap, and reports elapsed time, source/candidate rows,
logical/artifact bytes, compression ratio, and restore verification. It never
deletes source or target data.

## Frozen production evidence

Generic usage/ops promoted archive is a **frozen historical closeout**, declared by
`pipeline_status.yaml` → `archive_mode: frozen`. It is not an active tail pipeline:
new rows belong to PostgreSQL hot retention, while the checked-in final tail remains
the immutable historical coverage boundary.

| Owner | Responsibility |
| --- | --- |
| `OpsCleanupService` | Daily age retention: capped row DELETE + whole-partition DROP when `upper_bound <= now - hot_layer_days` |
| `ops/archive/` CLIs | Export/promote/closeout/hold **only** on the re-export exception path; **no** DROP CLI |
| `ops/archive/evidence/` + `pipeline_status.yaml` | Archive evidence SSOT (not live prod) |

Verify steady state:

```bash
python3 ops/observability/data_layer_archive_health.py
```

All must hold:

- `archive_mode=frozen`
- `closeout_complete=true`
- `tail_export_complete=true` and every final tail batch is promoted
- `cleanup_release_complete=true`
- both closeout restore proofs remain present and correctly bound
- `evidence_errors=[]`

Frozen mode deliberately does not compare the final tail cutoff with today's hot-layer
window and does not require a new restore rehearsal every 30 days. Missing/corrupt
ledgers, bindings, release evidence, or restore proofs still fail closed.

Do not treat hold **apply** receipts as current prod state. `archive_health` binds the
**release** receipt to the latest valid hold apply receipt. QA lifecycle is separate:
[`ops/qa/README.md`](../qa/README.md).

Physical DROP semantics: `docs/approved/design-prod-archive-bucket.md` §分区回收门禁.
Promote ledger `drop_ready` proves export evidence only; it does **not** authorize deletion.

## Break-glass re-export tooling

The export/promote/closeout/hold CLIs remain available for an explicitly approved
historical-evidence repair. Frozen mode does not run or request routine post-legacy
cold re-export, and archive health does not infer a new gap from the passage of time.

1. **Hold** — `data_layer_archive_cleanup_hold.py` plan → apply → verify (new receipt)
2. **Export** — `data_layer_archive_prod_export.py run-batch` with scope `post_legacy_cold`
3. **Promote** — `data_layer_archive_promote_batch.py promote-ledger`
4. **Closeout** — `data_layer_archive_closeout.py` per ops table (hold still active)
5. **Release** — `data_layer_archive_cleanup_hold.py release` after
   `data_layer_retention_activation.py` plan; persist receipt under
   `pipeline_status.yaml` → `cleanup_release_receipt_glob`

Refresh ledgers/receipts under `ops/archive/evidence/` after each batch.
First-time canary or legacy-scope export mechanics: CLI `--help` on
`data_layer_archive_prod_canary.py` and `data_layer_archive_prod_export.py`.
Design baselines: `docs/approved/design-data-layer-prod-export-canary.md`,
`docs/approved/design-prod-archive-bucket.md`.

Long-term archive bucket CFN (once per account):

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-stage0-archive \
  --template-file deploy/aws/cloudformation/stage0-archive.yaml \
  --parameter-overrides AppInstanceRoleArn=<prod InstanceRole ARN>
```
