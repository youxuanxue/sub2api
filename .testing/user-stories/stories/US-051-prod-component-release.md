# US-051 Prod Component Release

- ID: US-051
- Title: Deploy the changed prod components with independent QA lifecycle
- Priority: P1
- As a / I want / So that: As a gateway operator, I want gateway deployment isolated from raw maintenance with explicit raw storage migration gates, so that gateway releases do not wait for unrelated QA work.
- Trace: docs/approved/qa-raw-only.md; user approval in this session
- Risk Focus:
  - 逻辑错误：组件基线误判；行为回归：QA 删除守卫失效。
  - 安全问题：配置漂移；运行时：排空失败与取消恢复。

## Acceptance Criteria

1. AC-001: Gateway blue/green deploys never change raw archive coordinates.
2. AC-002: Raw maintenance remains the only archive lifecycle owner.
3. AC-003: Removed Bundle, worker, queue, and product export surfaces stay absent.
4. AC-004: Cutover and completion retain the existing drain and rollback safety.

## Linked Tests

- `ops/stage0/test_prod_component_release.py`::`RawArchiveReleasePlanTest.test_rollout_is_raw_archive_contract`
- `ops/stage0/test_prod_component_release.py`::`BlueGreenCompletionTest.test_us051_completion_waits_and_propagates_drain_failure`
- `ops/stage0/test_bluegreen_host_runtime.py`::`HostRuntimeTest.test_both_colors_receive_readonly_identity_without_secret_contents`
- `backend/internal/server/routes/qa_retired_routes_test.go`::`TestRetiredQAProductRoutesReturn404`
- `frontend/e2e/qa-retirement.e2e.ts`::`QA retirement preserves key and admin edits`
- Run: `python3 -m unittest ops.stage0.test_prod_component_release ops.stage0.test_deploy_via_ssm_bluegreen`

## Assertions

- Failed or drifted acceptance cannot advance the verified component baseline.
- Only successful drain and required observations permit release completion.
- QA runtime installation failure restores the prior pin and host files together.

## Evidence

Raw-only 验证：Playwright 桌面/手机 Keys 与管理员编辑通过；capture/archive/lifecycle、退役路由和坐标保护测试已运行。存量复制与其测试随用户确认的自然到期方案移除；新增归档切换的验收边界见 raw-only 契约。完整 preflight 证据在 PR 验证段记录；生产切换尚未执行。

## Status

- [x] Done: repository implementation and local verification; production rollout remains separate.
