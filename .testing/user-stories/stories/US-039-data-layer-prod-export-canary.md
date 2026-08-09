# US-039-data-layer-prod-export-canary

- ID: US-039
- Title: Data-layer 生产只读导出 canary
- Priority: P0
- As a / I want / So that:
  作为运维者，曾需要 prod canary export 能力；**已 superseded**，日常以 `archive_health` steady state 为准。

- Trace:
  - SSOT：`ops/archive/README.md`、`ops/archive/pipeline_status.yaml`、`ops/archive/evidence/`
  - 设计：`docs/approved/design-data-layer-prod-export-canary.md`（Exception path / CLI `--help`）

- Risk Focus:
  - 不适用：Story 已 Archived；运行时门禁见 US-036 与 `archive_health`。

## Acceptance Criteria

1. **AC-001（Archived）**：Given prod steady state，When `python3 ops/observability/data_layer_archive_health.py`，Then 三 flag 绿且 `evidence_errors=[]`。

## Assertions

- 本 Story 不提供 agent 操作路径；re-export 例外见 `ops/archive/README.md` Exception path。

## Linked Tests

- `ops/observability/test_data_layer_archive_health.py`::`DataLayerArchiveHealthTest.test_checked_in_ledgers_pass_their_owner_validator`

运行命令：

```bash
python3 ops/observability/test_data_layer_archive_health.py
```

## Status

- [x] Archived — superseded by prod steady state + `ops/archive/evidence/` SSOT.
