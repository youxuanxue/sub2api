# US-044-qa-lifecycle-single-owner-and-export-contract

- ID: US-044
- Title: QA 生命周期单一 owner 与唯一用户导出契约
- Priority: P0（生产数据生命周期、授权与磁盘安全）
- As a / I want / So that:
  作为 **TokenKey 用户与生产运维者**，我希望 **QA 生命周期只由一个 approved SSOT 管理，用户只通过受授权的 API-key trajectory export 导出最近 24 小时数据**，**以便** 旧 archive/purge/self-export 路径不能绕过授权、扩大窗口、在 prod 执行无界操作或重新制造多套 retention 语义。
- Trace:
  - 设计锚点：`docs/approved/design-prod-qa-24h-s3-lifecycle.md`
  - 已实现导出基线：`/api/v1/users/me/qa/traj/*`
  - 机械门禁：`scripts/checks/qa-lifecycle-ssot.py`

## Scope Boundary

- 本 Story 只记录已实现的 prod trajectory export 基线，不定义最终用户读取路径。
- 未来 S3-only list/detail/export 目标只由主 QA 设计定义；当前 prod route 和测试不能覆盖该目标。

- Risk Focus:
  - 逻辑错误：per-key 导出重新读取全部 retained history，或通用 data-layer 工具重新接管 QA。
  - 行为回归：普通 `/users/me/qa/export`、daily auto-export 或 destructive purge 被重新注册。
  - 安全问题：仅靠 UI 隐藏，伪造请求绕过 `traj_export_enabled`、key ownership 或 projectable-platform 检查。
  - 运行时问题：已实现基线中的 prod 大文件构建/下载路径被扩展，或被误写成未来 S3-only 目标。

## Acceptance Criteria

1. **AC-001（唯一用户 surface）**：Given 用户 QA 路由注册，When 枚举 dual-auth routes，Then 只保留 `/users/me/qa/traj/*`；普通 self-export 与其下载路由不存在。
2. **AC-002（完整服务端授权）**：Given enqueue/list/get/download 请求，When `traj_export_enabled=false`，Then 服务端返回 403；enqueue 还必须拒绝 missing、foreign、deleted、no-group 或 unprojectable API key，且不创建 job。
3. **AC-003（固定 per-key 24h）**：Given caller 传入更宽的内部时间范围且同 key 同时有当前与 30 小时前记录，When enqueue，Then service 覆盖 caller 窗口并只导出最近 24 小时 source records；空 key fail closed。
4. **AC-004（删除冲突能力）**：Given 当前仓库，When运行 QA lifecycle sentinel，Then旧方案文档、US-033、destructive purge、普通 self-export、daily auto-export 和 auto/manual wire/UI 语义均不能重新出现。
5. **AC-005（generic data-layer 分权）**：Given usage/ops archive rehearsal 与 retention inventory，When输入或扫描 QA dataset，Then rehearsal 拒绝 QA，inventory 不查询 `qa_records`、不读取 QA retention 参数或 Blob filesystem。
6. **AC-006（迁移兼容受限）**：Given Phase 1/2/3/4 替代物尚未全部上线，When审查仍存在的 Edge stale cleanup、只读 dump、in-prod trajectory worker 与 prod stale timer，Then它们只出现在 approved 退役矩阵中，带明确 Phase/删除门禁且不具备第二 SSOT 权限。

## Assertions

- `traj_export_enabled` 只控制用户 trajectory export，不过滤 capture、raw archive 或 cleanup。
- projectable platform allowlist 直接读取 `engine.TrajProjectablePlatforms()`，不得复制列表。
- synth 字段继续 capture/project，但不能作为绕过 24 小时窗口的导出 selector。
- 用户 ZIP bucket、raw QA bucket、generic usage/ops archive bucket 与 pgdump bucket边界分离。
- 本 Story 不启用 AWS 资源、不部署、不清理线上 QA，也不验收未来 S3-only 用户面。

## Linked Tests

- `backend/internal/server/routes/user_tk_routes_test.go`::`TestUS044_RegisterTKUserDualAuthRoutes_OnlyTrajectoryExportRemains`
- `backend/internal/handler/qa_handler_test.go`::`TestUS044_ExportSelfTrajectory_RejectsForeignAPIKey`
- `backend/internal/handler/qa_handler_test.go`::`TestUS044_ExportSelfTrajectory_RejectsUnprojectablePlatform`
- `backend/internal/handler/qa_handler_test.go`::`TestUS044_ExportSelfTrajectory_RejectsNoGroupOrDeletedAPIKey`
- `backend/internal/handler/qa_handler_test.go`::`TestUS044_TrajectoryExportReadSurfaces_ForbiddenWhenSwitchOff`
- `backend/internal/observability/qa/service_traj_export_job_test.go`::`TestUS044_EnqueueExport_RejectsMissingAPIKey`
- `backend/internal/observability/qa/service_traj_export_job_test.go`::`TestUS044_EnqueueExport_ClampsTo24HoursAndSurvivesRestart`
- `ops/archive/test_data_layer_archive_rehearsal.py`::`DataLayerArchiveRehearsalTest.test_us044_qa_dataset_is_rejected_by_generic_data_layer_rehearsal`
- `ops/observability/test_data_layer_retention_inventory.py`::`DataLayerRetentionInventorySafetyTest.test_probe_is_read_only_and_whitelist_bounded`

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/observability/qa ./internal/handler ./internal/server/routes
cd ..
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 ops/archive/test_data_layer_archive_rehearsal.py
python3 ops/observability/test_data_layer_retention_inventory.py
cd frontend
pnpm typecheck
pnpm exec vitest run src/api/__tests__/qaTraj.spec.ts src/composables/__tests__/useTkExportPanel.spec.ts
```

## Evidence

- focused Go、Python、frontend typecheck/API/composable tests 与完整 preflight 在本分支实际运行。
- `UseKeyModal.spec.ts` 的既有 4 个失败不涉及本次修改的 `ExportPanel.vue`；直接相关 frontend tests 单独通过。
- 公共契约删除提交必须包含 `contract-deletion-notice`。

## Status

- [x] Done — 已实现的 prod trajectory export 基线由行为测试、SSOT sentinel 和 preflight 验证；未来目标由主 QA 设计验收。
