# US-040-ops-legacy-export-promote

- ID: US-040
- Title: Legacy 冷数据分批 export 与长期 archive promote
- Priority: P0
- As a / I want / So that:
  作为运维者，曾需要 legacy export/promote 证据链；**已 superseded**，steady state 由 `archive_health` 机械校验。

- Trace:
  - SSOT：`ops/archive/evidence/` + `ops/archive/pipeline_status.yaml`
  - 设计：`docs/approved/design-prod-archive-bucket.md`

- Risk Focus:
  - 不适用：Story 已 Archived；`drop_ready` 仍不授权 DROP（见 approved 设计）。

## Acceptance Criteria

1. **AC-001（Archived）**：Given checked-in repo evidence，When `python3 ops/observability/data_layer_archive_health.py`，Then closeout + tail + release 三 flag 绿。

## Assertions

- export/promote CLI 仅用于 Exception path；日常 retention 由 `OpsCleanupService` 负责。

## Linked Tests

- `ops/observability/test_data_layer_archive_health.py`::`DataLayerArchiveHealthReleaseTest.test_release_receipt_must_bind_to_latest_hold`

运行命令：

```bash
python3 ops/observability/test_data_layer_archive_health.py
```

## Status

- [x] Archived — legacy export/promote 证据已收口至 `ops/archive/evidence/`。
