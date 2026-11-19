# Data-layer archive rehearsal

SSOT：`pipeline_status.yaml`（repo 证据路径与 hot-layer retention 天数，与
`data_layer_archive_rehearsal.py` 机械对齐）。QA 生命周期见 [`ops/qa/README.md`](../qa/README.md)。

本目录保留 **nonprod rehearsal**、**COS host-config 上传** 与 **frozen 历史证据**。
生产 export / canary / hold / promote / closeout CLI 已于 2026-11-19 退役（见
[`docs/decisions/declined.md`](../../docs/decisions/declined.md)）：不再迁腾讯、不再作为现网必做。
库级保护以 COS pgBackRest full+WAL（+ `cos_restore_acceptance`）为准；QA 走 node1。

Retention day defaults: `pipeline_status.yaml`（preflight:
`scripts/checks/data-layer-archive-ssot.py`）。

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
Production access is not available through the rehearsal CLI.

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
`pipeline_status.yaml` → `archive_mode: frozen`. It is not an active pipeline.
Checked-in ledgers under `ops/archive/evidence/` remain as historical coverage
boundary only — do not refresh them and do not reintroduce export/canary CLIs.

| Owner | Responsibility |
| --- | --- |
| `OpsCleanupService` | Daily age retention: capped row DELETE + whole-partition DROP when `upper_bound <= now - hot_layer_days` |
| `ops/archive/evidence/` + `pipeline_status.yaml` | Frozen historical archive evidence (not live prod) |
| pgBackRest + `cos_restore_acceptance` | Live library-level protection |

## 对象归档目的地（2026-11-17）

新对象归档 **只允许腾讯 COS**，见
[`docs/approved/design-cos-object-archive.md`](../../docs/approved/design-cos-object-archive.md)。

| 项 | 值 |
|---|---|
| 桶 | `tokenkey-prod-ops-archive-1320330536`（美西） |
| 设置 | `ops/archive/cos-archive.json` |
| Identity | `ops/archive/cos_archive_identity.py`（AWS 目的地 fail-closed） |
| host-config 上传 | `python3 ops/archive/upload_host_config.py --file … --label …`（默认 plan；`--apply` 才写） |
| 凭据 | 复用 backup CAM：prod data 已装 `/etc/tokenkey/cos-archive.env`（root 0600；与 `cos-backup.env` 同密钥，不新建子用户）。控制器可用 `TK_COS_ARCHIVE_CREDENTIALS` |

历史 AWS CFN `tokenkey-stage0-archive` 仅作风控收尸（30 天自删，实例角色只读），不再作为新写入目的地。
