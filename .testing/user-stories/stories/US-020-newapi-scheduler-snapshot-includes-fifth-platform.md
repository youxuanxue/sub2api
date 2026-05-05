# US-020-newapi-scheduler-snapshot-includes-fifth-platform

- ID: US-020
- Title: 调度快照重建必须包含第五平台 newapi（防 PlatformNewAPI 漂移性丢失）
- Version: V1.4
- Priority: P0
- As a / I want / So that: 作为 sub2api 终端用户，我希望调度器在重建账号快照时把 `PlatformNewAPI` 一并纳入，以便在群组激活/账号变更后 newapi 账号的可调度性立即生效，而不是被"4 平台硬编码列表"漂移导致 newapi 池被静默清空。
- Trace: 实体生命周期（账号 → 调度池）+ Bug A 复现于 `scheduler_snapshot_service.go::rebuildByGroupIDs` 与 `defaultBuckets`。修复策略：用 `AllSchedulingPlatforms()` 单一源消除硬编码列表。
- Risk Focus:
  - 行为回归：任何对 4 元素 platform 列表的硬编码（`[]string{anthropic, openai, gemini, antigravity}`）都会让 newapi 在快照重建后从池中消失，触发 "newapi pool empty" 错误。
  - 逻辑错误：默认桶（`defaultBuckets`）漏掉 `PlatformNewAPI` 会让 group activation 后第一次调度找不到桶位。
  - 安全问题：不适用。

## Acceptance Criteria

1. AC-001 (正向)：Given `AllSchedulingPlatforms()` 被调用，Then 返回值包含 `PlatformAnthropic`、`PlatformOpenAI`、`PlatformGemini`、`PlatformAntigravity`、`PlatformNewAPI`（5 个 canonical 平台齐全）。
2. AC-002 (回归 / Bug A 复现)：Given 一个仅包含 newapi 账号的 group，When `SchedulerSnapshotService.rebuildByGroupIDs(groupID)` 重建快照，Then snapshot 中该 group 的 newapi bucket 被正确填充（修复前为空 — 是被硬编码 4 平台列表漂移掉的）。
3. AC-003 (回归 / 默认桶)：Given `defaultBuckets()` 被调用，Then 返回的 map 含 `PlatformNewAPI` key（修复前缺失 — group activation 时无桶位可写）。

## Assertions

- AC-001：`require.Contains(platforms, PlatformNewAPI)` 且四个老平台均在。
- AC-002：snapshot 的 newapi 桶非空且包含被插入账号的 ID。
- AC-003：`require.Contains(buckets, PlatformNewAPI)` 且 value 是空 slice（不是 nil 也不是缺 key）。
- 失败时 testify `require` 立即报错并 exit ≠ 0。

## Linked Tests

- `backend/internal/service/scheduler_snapshot_platforms_test.go`::`TestAllSchedulingPlatforms_IncludesNewAPI` *(AC-001)*
- `backend/internal/service/scheduler_snapshot_platforms_test.go`::`TestRebuildByGroupIDs_RebuildsNewAPIBucket` *(AC-002)*
- `backend/internal/service/scheduler_snapshot_platforms_test.go`::`TestDefaultBuckets_IncludesNewAPI` *(AC-003)*
- 运行命令：`cd backend && go test -tags=unit -v -run 'TestAllSchedulingPlatforms_|TestRebuildByGroupIDs_|TestDefaultBuckets_' ./internal/service/`

## Evidence

- CI/preflight 中对应 Go test 输出

## Status

- [x] InTest
