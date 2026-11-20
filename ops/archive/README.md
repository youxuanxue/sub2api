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
| 入口证书打包 | `python3 ops/archive/pack_entry_host_config.py inventory\|pack\|plan-upload`（明文 `.tar.gz`；[`design-prod-entry-multi-app.md`](../../docs/approved/design-prod-entry-multi-app.md) §2.1；2026-11-19 决定不做操作员 RSA） |
| 首次/续期上传 | `TK_HOST_CONFIG_APPLY=1 bash ops/archive/apply_entry_host_config_offsite.sh`（入口 pack → 数据机拉取 → COS `--apply`） |
| host-config 列表 | `python3 ops/archive/list_host_config.py`（只读 key/size/LastModified） |
| 入口 inventory 探针 | `run-probe.sh --target prod --node entry --script ops/archive/probe-entry-host-config-inventory.sh` |
| COS 列表探针 | `run-probe.sh --target prod-data --script ops/archive/probe-list-host-config.sh` |
| 凭据 | 与库备份同一文件：数据机 `/etc/tokenkey/cos-backup.env`（root 0600）。不另建归档子用户；可用 `TK_COS_ARCHIVE_CREDENTIALS` 覆盖 |

### 入口离机备份（明文 tar → COS）

1. 只读 inventory：`bash ops/observability/run-probe.sh --target prod --node entry --script ops/archive/probe-entry-host-config-inventory.sh`
2. 上传（需确认口令）：  
   - prod：`TK_HOST_CONFIG_APPLY=1 bash ops/archive/apply_entry_host_config_offsite.sh`  
   - cn-prod：`TK_HOST_CONFIG_APPLY=1 SITE=cn-prod bash ops/archive/apply_entry_host_config_offsite.sh`  
   入口机无 COS 凭据：私网一次性 HTTP 把 tar 交给数据机，由数据机读 `cos-backup.env` 执行 `upload_host_config.py --apply`。
3. 续期后重复。重建入口机：用上传 receipt 里的 key（或 metadata sha256）`aws s3 cp` 拉回后解开还原 `caddy/`。  
   注：`cos_archive_identity` 使用裸 COS host + `addressing_style=virtual`。
   勿把桶名塞进 `--endpoint-url` 且不设 virtual（List/Put 会 path-style 成
   `/{bucket}/…` 假 key）。
4. 不打包应用机/数据机全量 `.env`。门禁 = COS 读权限（非本机私钥）。

历史 AWS CFN `tokenkey-stage0-archive` 仅作风控收尸（30 天自删，实例角色只读），不再作为新写入目的地。
