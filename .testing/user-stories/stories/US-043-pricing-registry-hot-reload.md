# US-043-pricing-registry-hot-reload

- ID: US-043
- Title: Global pricing has one registry owner and protected hot reload
- Priority: P0 (计费正确性与全局价格发布)
- As a / I want / So that:
  作为 **TokenKey 价格运营与计费负责人**，我希望 **只审核和修改一个完整 registry，并在合并后安全热生效**，**以便** 新模型和改价不再多文件维护，同时外部价格源、旧发版或本地运维文件都不能静默改变用户账单。
- Trace:
  - 设计锚点：`docs/approved/pricing-registry-hot-reload.md`
  - 价格 owner：`backend/internal/service/tk_pricing_overlay.json`
  - Runtime owner：完整 registry snapshot；settings 仅为 protected-main artifact 的确定性物化。
  - 传感器：Provider/LiteLLM 只生成差异和候选 PR，不进入 runtime billing。
- Risk Focus:
  - 逻辑错误：requested model、routed model 与 billing owner 分裂，导致错档计费；某个价格维度在 registry 投影时丢失。
  - 行为回归：从外部源并集切到完整 registry 时改变现有余额预占、结算、展示、渠道覆盖或 family-floor 行为。
  - 安全问题：未合并分支、本地文件、损坏 digest 或旧 schema 写入全局 runtime。
  - 运行时：跨副本热更新出现半更新、无效 snapshot 清空价格、旧发版覆盖新价格或 publisher 乱序降级。

## Acceptance Criteria

1. **AC-001（唯一全局 owner）**：Given 任意全局价格解析，When 外部价格文件与 registry 不同，Then billing 和 catalog 只采用 active registry；Go 只保留 alias → registry owner 关系，不含 canonical 数值价格。
2. **AC-002（迁移奇偶）**：Given 迁移前当前有效价格和 family-floor，When 构建完整 registry，Then requested/routed/owner 与所有计费维度保持一致；任何有意修正必须单独列明。
3. **AC-003（受保护热更新）**：Given registry PR 已合并到 protected main，When publisher 构建并写入 snapshot，Then source commit 与 exact-byte digest 可验证、所有副本原子切换，且无需应用发版。
4. **AC-004（last-known-good）**：Given legacy、损坏、digest 不符或不支持 schema 的 runtime blob，When replica reload，Then 保留上一份有效 snapshot，并返回可观测错误而不是清空或部分合并。
5. **AC-005（防降级）**：Given active runtime 比部署镜像内嵌 registry 更新，When 部署或回滚应用，Then部署流程不写价格 setting，新 snapshot 不被旧镜像覆盖；价格回滚只能通过 Git revert 后重新发布。
6. **AC-006（路由计费同 owner）**：Given `gpt-5.5-pro` 兼容请求路由到 `gpt-5.5`，When 预占与结算，Then 都按 routed `gpt-5.5` registry owner 计费，不采用 provider feed 的独立 Pro 行。
7. **AC-007（传感器不决策）**：Given Provider/LiteLLM 出现新模型或改价，When sensor 运行，Then 只生成确定性 diff/候选 PR；未合并内容不进入 runtime，也不改变 serving。
8. **AC-008（单文件改价）**：Given 普通已上架模型改价，When 更新 owner，Then人工价格修改只涉及 registry 文件，校验、runtime envelope、diff 报告均由机器派生。

## Assertions

- Runtime envelope 的 schema、source commit 与 exact registry digest 缺一不可。
- Snapshot 的 models、全局 pricing policy 与 metadata 在同一临界区切换。
- External parse/sync 可以保留用于 sensor 和上游兼容，但它的结果在 effective map 中被完整 registry 替换。
- `channel_model_pricing` 只覆盖指定 scope，缺失维度继续遵循现有 resolver 契约。
- deliberate-free 与 unknown-zero 必须可区分；unknown-zero 仍 fail closed。
- publisher 写后必须读回并核对 source commit 与 digest。

## Linked Tests

- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestUS043_RegistryReplacesExternalPricing`
- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestUS043_RuntimeSnapshotAtomicallyReplacesRegistry`
- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestUS043_InvalidAndLegacyRuntimeKeepLastKnownGood`
- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestReloadTKOverlayRuntimeKeepsLKGWhenSettingTemporarilyDisappears`
- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestReloadTKOverlayRuntimeSerializesGetterThroughSwap`
- `backend/internal/service/pricing_service_tk_overlay_runtime_test.go`::`TestConcurrentRegistryReadDuringExactSwap`
- `backend/internal/service/billing_service_tk_registry_alias_test.go`::`TestUS043_GPT55ProAliasBillsRoutedRegistryOwner`
- `backend/internal/service/billing_service_tk_registry_alias_test.go`::`TestUS043_LegacyFallbackNumbersCannotAffectBilling`
- `backend/internal/service/billing_service_tk_registry_alias_test.go`::`TestUS043_RegistryBackedLegacyMatcherKeepsExplicitOwner`
- `backend/internal/service/billing_service_tk_registry_alias_test.go`::`TestUS043_RegistryAliasPriceAndPolicyUseOneSnapshot`
- `backend/internal/service/billing_service_tk_image_token_settlement_test.go`::`TestUS043_BothGatewayImageFunnelsUseTokenSettlement`
- `backend/internal/service/billing_service_tk_image_token_settlement_test.go`::`TestUS043_GatewayMissingImageTokensReturnsBillingError`
- `backend/internal/service/pricing_catalog_tk_test.go`::`TestUS043_PublicCatalogSurfacesImageTokenSettlementDimensions`
- `ops/pricing/test_manage_overlay_runtime.py`::`test_sync_verifies_readback_after_one_atomic_write`
- `ops/pricing/test_manage_overlay_runtime.py`::`test_sync_refuses_to_downgrade_newer_runtime_source`
- `ops/pricing/test_pricing_registry_sensor.py`::`test_candidate_updates_only_existing_owner_billable_fields`
- `scripts/checks/test_pricing_registry_publication.py`::`test_rejects_non_main_or_multi_file_publication_trigger`
- `scripts/checks/test_pricing_registry_publication.py`::`test_rejects_deploy_price_write`
- `scripts/checks/test_pricing_registry_publication.py`::`test_rejects_sensor_aws_or_runtime_publication_capability`
- `scripts/checks/test_pricing_overlay.py`::`PricingRegistryMigrationParityTest.test_reconstructs_legacy_fill_only_precedence`
- `scripts/checks/test_pricing_overlay.py`::`PricingRegistryMigrationParityTest.test_reports_approved_and_unapproved_price_deltas_separately`
- `scripts/checks/test_pricing_overlay.py`::`PricingRegistryMigrationParityTest.test_materializes_legacy_openai_policy_before_comparison`

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/service
cd ..
python3 -m unittest scripts.checks.test_pricing_overlay \
  scripts.checks.test_pricing_registry_publication \
  ops.pricing.test_manage_overlay_runtime \
  ops.pricing.test_pricing_registry_sensor
python3 scripts/checks/pricing-registry-migration-parity.py \
  --output .testing/user-stories/attachments/US-043-pricing-registry-migration-parity.json
```

## Evidence

- 不可变迁移报告：`.testing/user-stories/attachments/US-043-pricing-registry-migration-parity.json`。报告固定旧实现 commit、初始 registry commit、经复审修正后的 registry blob、外部源 commit 与 exact-byte SHA-256；比较 340 个 owner / 1252 个有效维度，未批准价差、缺失 owner、意外 owner 和全局 policy 差异均为 0，仅保留获批的 `kimi-k2.6` 4 个价差维度。
- 聚焦 Go/Python 行为测试、registry gate、上游 merge-tree 审计与项目 preflight 由本 PR 执行并记录。
- 本变更无 Web surface；提交消息使用 `no-web-impact` 作为机械锚点。

## Status

- [x] Done — 验收标准均已由自动化测试与 preflight 验证。
