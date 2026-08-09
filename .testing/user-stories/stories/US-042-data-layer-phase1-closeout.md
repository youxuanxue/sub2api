# US-042-data-layer-phase1-closeout

- ID: US-042
- Title: Data-layer 第一阶段安全收口
- Priority: P0
- As a / I want / So that:
  作为运维者，曾需要 Phase1 分区/归档/telemetry 收口；**prod steady state 已达成**，本 Story 归档为历史验收锚点。

- Trace:
  - SSOT：`ops/observability/data_layer_archive_health.py`、`ops/observability/data_layer_safety_verdict.py`
  - 设计：`docs/approved/design-data-layer-phase1-closeout.md`

- Risk Focus:
  - 不适用：Story 已 Archived；独立 fail-closed findings 仍由 safety verdict 探针输出。

## Acceptance Criteria

1. **AC-001（Archived）**：Given prod steady state，When archive health + safety verdict probes，Then archive 三 flag 绿且 capacity/telemetry 独立 finding 不互相覆盖。

## Assertions

- RDS 第二阶段仍 hold；本 Story 不再承载日常 agent 操作说明。

## Linked Tests

- `ops/observability/test_data_layer_safety_verdict.py`::`DataLayerSafetyVerdictTest.test_capacity_independent_failures_are_separate_findings`

运行命令：

```bash
python3 ops/observability/test_data_layer_safety_verdict.py
```

## Status

- [x] Archived — Phase1 closeout shipped；steady state 见 `archive_health`。
