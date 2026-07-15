# US-035-model-surface-bundle-activation

- ID: US-035
- Title: Model surface bundle activation 与 generic deploy 解耦
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 发布运维者**，我希望 **generic deploy/rollback 不依赖 live model_mapping，而模型面通过 checksummed bundle 和独立证据显式激活**，**以便** 发布与配置不互锁，同时不把未实测、未定价模型暴露给线上用户。

- Trace:
  - 设计锚点：`docs/approved/model-surface-activation-contract.md`
  - Go owner：`backend/internal/service/account_model_mapping_ssot_tk.go`
  - 激活入口：`ops/pricing/modelops.py activate`
- Risk Focus:
  - 逻辑错误：current/target delta、required floor、forbidden policy 或 compatible extras 计算错误。
  - 行为回归：generic deploy 被 live mapping gate 阻塞，或 Edge 诊断/apply CLI 被误删。
  - 安全问题：陈旧、digest 不匹配、同源伪独立证据绕过写入门禁。
  - 运行时问题：runtime replacement shadow target artifact，或 apply 后 gate 未收敛。

## Acceptance Criteria

1. **AC-001（build once）**：Given Go model owner，When release/preflight 生成 bundle，Then artifact 可被 Python 独立校验且 rollout 不运行 Go helper。
2. **AC-002（正向 activation）**：Given current→target 新增 required mapping 且 probe/pricing evidence 新鲜、digest 匹配、来源独立，When 默认运行 `activate`，Then 只生成 prod plan 与只读 gate，不写线上状态。
3. **AC-003（负向 evidence）**：Given evidence 过期、target digest 不匹配、缺少 verdict/account 或 probe/pricing 同源，When activation 校验，Then 在 SSM/apply 前拒绝。
4. **AC-004（runtime shadow）**：Given prod 存在或在 pre-gate 后出现 `tk_account_model_mapping_runtime`，When activation 校验或写入 target bundle，Then 在账号写事务前拒绝并要求 fold-in 或 clear。
5. **AC-005（显式写入）**：Given 人工审过 dry-run，When 提供 `yes-activate-model-surface`，Then 只向 prod apply，且 post-apply release gate 必须通过；generic deploy/rollback 不调用该链。
6. **AC-006（live validity）**：Given live mapping 含完整 required floor、无 forbidden 项且有兼容 extras，When check/apply，Then extras 不报 drift且不被删除；forbidden 项被 plan 删除。
7. **AC-007（Admin contract）**：Given 任一 first-class account platform，When 调用 `GET /admin/accounts/:id/models`，Then 每个 option 仅返回 `id` 与 `display_name`。

## Assertions

- Bundle schema、canonical SHA-256、mapping 字符串形状与 required/forbidden 冲突全部 fail closed。
- Evidence 同时绑定 current/target floor digest，24 小时 freshness 由代码常量和测试共同约束。
- Activation 固定一次解析出的 prod instance，并在账号写事务内锁表复核 runtime shadow；不生成 Edge target，不复用 generic deploy workflow。
- Mapping reconciliation 保留 compatible extras，只补 required、改错 target、删 forbidden。
- Admin response 对七个平台逐项断言精确两字段。

## Linked Tests

- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_valid_evidence_builds_activation_delta`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_invalid_evidence_is_rejected`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_runtime_shadow_is_rejected`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_runtime_shadow_stops_before_apply`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_commands_are_prod_only_and_confirmed`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_confirmed_apply_pins_instance_and_requires_post_gate`
- `ops/pricing/test_model_activation.py`::`ModelActivationTest.test_us035_self_digested_invalid_bundle_is_rejected`
- `backend/internal/service/account_model_mapping_ssot_tk_test.go`::`TestModelSurfaceBundleForOps_DigestCoversCompleteFloor`
- `backend/internal/service/account_model_mapping_ssot_tk_test.go`::`TestModelSurfaceBundleForOps_RuntimeScopeIsFullReplacement`
- `backend/internal/handler/admin/account_handler_available_models_test.go`::`TestAccountHandlerGetAvailableModels_AllPlatformsUseMinimalOptionDTO`

运行命令：

```bash
python3 -m unittest ops/pricing/test_model_activation.py
python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest
cd backend && go test ./internal/service ./internal/handler/admin -run 'TestModelSurfaceBundleForOps|TestAccountHandlerGetAvailableModels'
```

## Status

- [x] InTest — 离线正负向、完整 unit/integration、preflight 已覆盖；真实 prod activation 仍须使用当次独立 probe/pricing evidence 与人工确认。
