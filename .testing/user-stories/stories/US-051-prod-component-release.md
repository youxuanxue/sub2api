# US-051 Prod Component Release

- ID: US-051
- Title: Deploy the changed prod components with independent QA lifecycle
- Priority: P1
- As a / I want / So that: As a gateway operator, I want one deployment entry that selects and verifies changed components, so that gateway releases do not wait for unrelated QA work.
- Trace: docs/approved/prod-component-release.md; user approval 2026-09-10
- Risk Focus:
  - 逻辑错误：组件基线误判；行为回归：QA 删除守卫失效。
  - 安全问题：配置漂移；运行时：排空失败与取消恢复。

## Acceptance Criteria

1. AC-001: Gateway-only changes never update or execute QA components.
2. AC-002: Worker-only and maintenance-only changes preserve the serving gateway.
3. AC-003: Shared dependencies and missing verification evidence require coordinated acceptance.
4. AC-004: Old worker tags cannot repeatedly trigger an already verified publisher canary.
5. AC-005: QA execution uses independent image, config, and network; missing pins fail closed.
6. AC-006: Cutover permits early smoke, but deployment completion requires successful drain.
7. AC-007: Legacy rollback preserves QA pins and pauses DROP; archive guards remain intact.

## Linked Tests

- `ops/stage0/test_prod_component_release.py`::`ComponentPlanTest.test_us051_gateway_only_never_restarts_or_runs_qa`
- `ops/stage0/test_prod_component_release.py`::`ComponentPlanTest.test_us051_worker_only_preserves_running_gateway_and_maintenance`
- `ops/stage0/test_prod_component_release.py`::`ComponentPlanTest.test_us051_shared_protocol_or_build_dependencies_coordinate`
- `ops/stage0/test_prod_component_release.py`::`ComponentPlanTest.test_us051_old_worker_does_not_repeat_verified_publisher_canary`
- `ops/stage0/test_prod_component_release.py`::`QARuntimeResolverTest.test_us051_runtime_does_not_depend_on_gateway_color`
- `ops/stage0/test_prod_component_release.py`::`BlueGreenCompletionTest.test_us051_completion_waits_and_propagates_drain_failure`
- `ops/stage0/test_prod_component_release.py`::`ComponentPlanTest.test_us051_legacy_rollback_keeps_qa_pins_and_pauses_drop`
- `backend/cmd/server/qa_maintenance_test.go`::`TestQAMaintenanceLegacyPauseSkipsDeletionEvenWithActivation`
- Run: `python3 -m unittest ops.stage0.test_prod_component_release ops.stage0.test_deploy_via_ssm_bluegreen ops.stage0.test_deploy_stage0_workflow ops.qa.test_qa_maintenance_phase2_runtime`

## Assertions

- Failed or drifted acceptance cannot advance the verified component baseline.
- Only successful drain and required observations permit release completion.
- QA runtime installation failure restores the prior pin and host files together.

## Evidence

2026-09-10: full repository preflight, 114 focused Python tests, Go maintenance unit tests,
shell syntax checks and actionlint passed. Production rollout is not yet performed.

## Status

- [x] Done: repository implementation and local verification; production rollout remains separate.
