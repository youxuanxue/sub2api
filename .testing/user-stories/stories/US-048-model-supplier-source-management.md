# US-048-model-supplier-source-management

- ID: US-048
- Title: 运营以供应源安全接入并热更新模型供应账号
- Priority: P0
- As a / I want / So that: 作为 TokenKey 运营，我希望只维护供应商通道、API Key、明确模型、采购比例和基础 priority，并在主动校验后把它们同步为现有账号配置，从而在不修改 scheduler、账号组、倍率和定价的前提下管理外部供给。
- Trace: `docs/approved/model-supplier-source-management.md`
- Risk Focus:
  - 逻辑错误：六档边界、同档合并、最终 priority 或先增后减顺序错误，导致模型进入错误账号或提前失去供给。
  - 行为回归：供应源引入第二套调度语义、覆盖账号组或普通运行字段、改变普通 NewAPI 网关行为，或者保存供应事实就直接改变线上账号。
  - 安全问题：凭证、指纹或上游原始错误经 API、日志或 Admin Audit Log 泄露，或通用账号入口绕过供应源 ownership。
  - 运行时问题：探测部分失败仍写账号、未经 Chat 正向证据写入非空 mapping、账号配置与协议能力半写、
    增加阶段失败后继续删除、重试不能收敛、空档账号继续可调度，或忽略 disabled/error 精确匹配而创建重复账号。

## Acceptance Criteria

1. AC-001 (正向): Given 运营新增供应源，When 只点击保存，Then 系统只写单表采购事实，凭证加密且不回显，新供应源默认 `base_priority=100`，不探测、不创建或修改账号。
2. AC-002 (优先级): Given 模型采购比例落入六个固定档位，When 查看表单与全局预览，Then 档位分别贡献 `1..6`，账号 priority 严格等于 `base_priority + discount_priority`；同源同档模型合并为一个目标账号，预览只比较供应源目标 priority，且 `base_priority + 6` 不得越过 PostgreSQL `INTEGER`。
3. AC-003 (探测门禁): Given endpoint、credential、模型 ID、模型增减、跨档，或非空受管账号的 `status/schedulable` 与目标不一致，When 点击“校验并同步”，Then 系统用带 source/band 身份的内存账号逐模型使用明确 upstream ID 真实探测；任一失败返回全部当次结果且不写账号。只有 Chat Completions 正向结果生成本次同步内部证据；FMGo Seedance 显式映射及任何 Responses/Anthropic 等非 Chat 成功证据都返回 `protocol_unsupported`，不猜协议、不写账号。
4. AC-004 (热更新): Given 全部结构探测成功，When 同步模型跨档或增减，Then 系统先以空 mapping、不可调度状态创建缺失账号；非空投影必须携带本次 Chat 正向证据，并在单账号事务内提交账号配置、Chat-only 现有协议能力、`InitialProbeCompleted=true`/`OfficialSeed=false` 证据和 scheduler outbox，再做配置读回确认而不进行写后二次网络探测，最后从旧档删除 mapping。单账号事务失败时已有账号保持旧配置、新账号保持空 mapping 不可调度；跨账号中途失败保留已完成增加、停止后续减少，返回 `failed_step + changes[]`，重试可继续收敛；空档账号保留并收敛为 `active + schedulable=false`。已选来源存在未保存表单修改时同步入口禁用并提示先保存，成功提示只能从当前结果派生。
5. AC-005 (隔离回归): Given 供应源保存、预览或同步，When 执行任一路径，Then 供应源不读取、比较、返回或写入账号组，专用投影读取不调用通用 `GetByID`，并且只选择同步所需配置字段，不读取倍率或无关运行字段；新账号保持未分组且不借用 `newapi-default`，普通账号创建失败契约不变；受管/预探测账号只声明 Chat Completions；通用 host 末尾 `/v1` 只在 OpenAI 受管路径规范化，`qianfan.baidubce.com` 解析为 BaiduV2 + 根 URL 并声明 `/v2/chat/completions`，普通 NewAPI 账号行为不变；系统不修改倍率、定价或 scheduler，scheduler 继续只按更小的原有账号 priority 先调度，不读取供应来源 Extra。
6. AC-006 (ownership): Given 账号 Extra 存在 `supplier_source_id`，When 普通账号单项、批量、复制、删除、从父账号创建影子账号、credentials/Extra/刷新凭证、状态、可调度或 CRS 导入覆盖尝试写入，Then service 层整体拒绝；普通创建和 CRS 导入不能伪造保留 Extra；供应源同步只走不携带 `rate_multiplier` 的窄写命令，只合并 `supplier_source_id`、`supplier_discount_band` 两个受管 Extra 并保留所有非受管 Extra，同时修复解析后的 NewAPI transport（OpenAI 或 Qianfan BaiduV2）与目标 `status/schedulable`。已有账号匹配覆盖全部未删除 NewAPI 候选，但只有 active 唯一精确匹配可接管；disabled/error 精确匹配返回冲突且不新建重复账号。恢复错误、配额重置、代理 fallback 与探测仍允许。真实账号 UI 显示“供应源托管”徽标和统一只读原因，点击后按 `source_id` 直接选中对应供应源。
7. AC-007 (安全): Given 创建、更新、探测或同步供应源，When API、日志和 Admin Audit Log 记录请求与结果，Then 不包含凭证明文、密文、HMAC 指纹或上游原始响应；探测只返回固定状态、协议分类和脱敏说明；HMAC 指纹跟随凭证加密密钥而非 JWT secret 的生命周期。
8. AC-008 (首批证据): Given 首批运营表和只读生产库存，When 记录验收结果，Then 三个案例必须各自提供完整准确的供应事实才能同步；信息不完整或不匹配时不扩大已有账号匹配、不改网关调度。佳杰/VSTECS 只保留两个最低合法比例 `0.50` 模型且因无生产凭证标记 `not_run`；FMGo 只保留 Seedance 显式双 ID 与 `protocol_unsupported`；百度千帆在凭证齐备时以 BaiduV2 transport 接管账号 90（channel_type=46），不以 OpenAI Chat 伪探测冒充成功。
9. AC-009 (上游发现): Given 已保存供应源，When 点击“校验并同步”，Then 系统先 `models-discover`：同步拉取上游 models 并规整已配置 ID，随后异步高并发探测**全部**未配置且可探测类型的候选（轮询 job 至完成）；仅探测通过的进入建议追加（默认 ratio `1.0`），探测失败或非 chat 类型不得建议追加；有规整变更时回填表单草稿要求保存，建议追加需运营主动加入表单；无规整变更且探测完成后继续既有投影同步。discover 不写供应源也不写账号。失败时管理页必须展示可读错误与 `failed_step`，不得只露出空标题。

## Assertions

- `purchase_ratio` 只接受空值或 `0 < ratio <= 1`；`1.00` 与空值进入档位 6。
- 同一供应源内 `client_model_id` 唯一，客户 ID 与上游 ID 必须逐行明确，不提供通用前缀替换器。
- 首批佳杰模型筛选是运营输入边界；格式未确认的 `43折` 等文本不参与自动比较。
- 保存、名称/priority 元数据同步和投影同步是三条明确路径；结构变化或非空账号的调度投影漂移需要探测。
- “校验并同步”内嵌 models-discover；建议追加必须以探测通过为门禁，不得仅凭 models 列表写入。
- 新建受管账号命令不接受 mapping；非空投影必须携带 Chat 正向探测证据，service 与 repository 双重拒绝绕过。
- 单账号账号配置、Chat-only capability、探测证据与 scheduler outbox 原子提交；读回是配置确认，不是写后二次探测。
- 供应源 `/v1` 规范化只由 OpenAI 受管路径触发；Qianfan 主机规范化为根 URL 并声明 `/v2/chat/completions`；普通 NewAPI base URL 不改写。
- `supplier_source_id + supplier_discount_band` 是受管账号逻辑身份；账号组不参与匹配或同步成败。
- 未删除的精确 endpoint + 凭证匹配不会因 disabled/error 被忽略；非 active 候选只报冲突，不绕过建号。
- 供应投影读取只选择同步所需配置字段，不查询账号组、倍率或无关运行字段，并清空 `AccountGroups`、`GroupIDs`、`Groups`。
- 供应源账号的配置更新不能调用通用 `AccountRepository.Update`。
- 供应源窄写只拥有 `supplier_source_id`、`supplier_discount_band`，不得删除或覆盖其他 Extra。
- 不存在 draft/ready/enabled、revision、activation、pause、供应来源调度层、组 diff 或跨账号回滚。
- Playwright 路由 mock 只证明真实 UI 行为，不证明上游渠道可用。

## Linked Tests

- `backend/internal/service/supplier_source_priority_test.go`::`TestUS048_DiscountBandBoundaries`
- `backend/internal/service/supplier_source_priority_test.go`::`TestUS048_SupplierPriorityIsBasePlusBand`
- `backend/internal/service/supplier_source_priority_test.go`::`TestUS048_SupplierPriorityStaysWithinPostgresIntegerRange`
- `backend/internal/service/supplier_source_service_test.go`::`TestUS048_CreateSupplierSourceEncryptsCredentialAndDefaultsBasePriority`
- `backend/internal/service/supplier_source_service_test.go`::`TestUS048_PriorityPreviewGroupsModelsBySourceBand`
- `backend/internal/service/supplier_source_fingerprint_test.go`::`TestUS048_SupplierCredentialFingerprintFollowsEncryptionKeyNotJWTKey`
- `backend/internal/repository/model_supplier_source_migration_integration_test.go`::`TestModelSupplierSourceMigrationCreatesOnlyTheSingleSourceTable`
- `backend/internal/repository/supplier_source_repo_test.go`::`TestSupplierSourceRepositoryCreateClassifiesIdentityConflict`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncGroupsSameBandModelsIntoOneAccount`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierProbeAccountCarriesManagedIdentity`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncProbeFailureReturnsEveryResultAndWritesNothing`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncMetadataOnlySkipsProbe`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncNameChangeUpdatesAccountWithoutProbe`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncMovesModelByAddingBeforeRemoving`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncStopsBeforeRemovalWhenVerifiedProjectionWriteFails`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncAdditionFailureStopsBeforeRemovalAndRetryConverges`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncClearsEmptyBandWithoutDeletingAccount`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncEarlyErrorsAlwaysReportFailedStep`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_NonActiveExactAccountMatchBlocksDuplicateSupplierAccountCreation`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_IncompatibleTransportExactMatchBlocksAdoptionWithoutRewritingAccount`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_QianfanBaiduV2ExactMatchIsAdopted`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_BaiduV2TransportAgainstOpenAISupplierIsRejected`
- `backend/internal/service/supplier_managed_transport_test.go`::`TestUS048_ResolveSupplierManagedTransportSelectsBaiduV2ForQianfan`
- `backend/internal/service/supplier_managed_account_commands_test.go`::`TestUS048_SupplierQianfanCreateUsesBaiduV2Transport`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_MultiBandExactAccountMatchBlocksDuplicateSupplierAccountCreation`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncProbesBeforeRepairingNonEmptySchedulingProjection`
- `backend/internal/service/supplier_source_sync_test.go`::`TestUS048_SupplierSyncRepairsEmptySchedulingProjectionWithoutProbe`
- `backend/internal/service/supplier_source_account_store_test.go`::`TestUS048_SupplierAccountStoreMatchesWithoutReadingGroups`
- `backend/internal/repository/account_repo_integration_test.go`::`TestAccountRepoSuite`（子测试：`TestUS048_SupplierProjectionReadsDoNotQueryAccountGroups`）
- `backend/internal/repository/account_repo_integration_test.go`::`TestAccountRepoSuite`（子测试：`TestUS048_SupplierAdoptionCandidatesExposeNonActiveCollisionsButNotDeletedAccounts`）
- `backend/internal/service/supplier_managed_account_commands_test.go`::`TestUS048_SupplierCreateStartsEmptyUngroupedAndUnschedulable`
- `backend/internal/service/supplier_managed_account_commands_test.go`::`TestUS048_OrdinaryCreateAccountStillFailsWhenDefaultGroupBindFails`
- `backend/internal/service/supplier_managed_account_commands_test.go`::`TestUS048_SupplierConfigurationUpdateUsesGroupFreeReadAndNarrowWrite`
- `backend/internal/service/supplier_managed_account_commands_test.go`::`TestUS048_SupplierConfigurationUpdateRequiresPassedChatProbe`
- `backend/internal/repository/account_repo_supplier_projection_test.go`::`TestUS048_SupplierConfigurationRepositoryRejectsUnverifiedNonEmptyMapping`
- `backend/internal/repository/account_repo_supplier_projection_test.go`::`TestUS048_SupplierConfigurationRepositoryWritesOnlyOwnedFields`
- `backend/internal/repository/account_repo_supplier_projection_test.go`::`TestUS048_SupplierProjectionReadSelectsOnlyRequiredConfigurationFields`
- `backend/internal/repository/account_repo_supplier_projection_test.go`::`TestUS048_SupplierMetadataRepositoryWritesOnlyNameAndPriority`
- `backend/internal/repository/account_repo_integration_test.go`::`TestAccountRepoSuite`（子测试：`TestUS048_SupplierConfigurationProjectionRebindsProtocolEndpointCapability`）
- `backend/internal/service/supplier_managed_account_guard_test.go`::`TestUS048_ManagedAccountRejectsEveryGenericUpdate`
- `backend/internal/service/supplier_managed_account_guard_test.go`::`TestUS048_ManagedAccountRejectsDirectExtraUpdateDeleteDuplicateAndSchedulable`
- `backend/internal/service/supplier_managed_account_guard_test.go`::`TestUS048_ManagedAccountRejectsGenericSparkShadowCreation`
- `backend/internal/service/supplier_managed_account_guard_test.go`::`TestUS048_GenericAccountServiceUsesTheSameSupplierOwnershipGuard`
- `backend/internal/service/supplier_managed_account_guard_test.go`::`TestUS048_UnmanagedAccountCannotForgeSupplierManagedExtra`
- `backend/internal/service/crs_sync_supplier_managed_test.go`::`TestUS048_CRSSyncRejectsSupplierManagedAccountOverwrite`
- `backend/internal/service/crs_sync_supplier_managed_test.go`::`TestUS048_CRSSyncRejectsReservedSupplierExtraOnNewAccount`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_SupplierModelMatchKeyNormalizesCaseAndSpaces`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_MatchSupplierUpstreamModelIDPrefersCanonicalID`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_BuildSupplierModelsListURLUsesBaiduV2Path`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_DiscoverModelsNormalizesAndSuggestsOnlyProbePassed`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_DiscoverModelsSuggestionsAloneDoNotBlockProjection`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_DiscoverModelsPreservesIntentionalClientUpstreamRemap`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_DiscoverModelsAuthFailureStopsWithoutSuggesting`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_StartDiscoverModelsProbesAllCandidatesAsynchronously`
- `backend/internal/service/supplier_models_discover_test.go`::`TestUS048_ExtractSupplierUpstreamModelEntriesKeepsType`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_FMGoSeedanceIsProtocolUnsupportedWithoutAccountWrite`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_SupplierManagedAccountDeclaresOnlyChatProtocol`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_SupplierManagedQianfanDeclaresBaiduV2ChatProtocol`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_UnmanagedQianfanIdentityKeyStaysStable`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_SupplierManagedOpenAIEndpointDoesNotDuplicateV1`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_SupplierProbeClassifiesEventsWithoutPersistingUpstreamDetail`
- `backend/internal/service/account_test_service_supplier_probe_test.go`::`TestUS048_SupplierProbeDoesNotLogRawUpstreamError`
- `backend/internal/service/newapi_bridge_usage_test.go`::`TestUS048_UnmanagedNewAPIOpenAIBaseURLRemainsUntouched`
- `backend/internal/service/gateway_scheduling_priority_test.go`::`TestSchedulingPriorityIgnoresAccountExtra`
- `backend/internal/handler/admin/supplier_source_handler_test.go`::`TestUS048_SupplierSourceResponsesExposeOnlyManagementFacts`
- `backend/internal/handler/admin/supplier_source_handler_test.go`::`TestUS048_SupplierSourceProbeFailureReturns422WithCompleteResult`
- `backend/internal/handler/admin/supplier_source_handler_test.go`::`TestUS048_SupplierSourceCreateRejectsMalformedPurchaseRatioAtJSONBoundary`
- `backend/internal/service/audit_log_test.go`::`TestRedactAuditBody_RedactsSupplierSourceCredential`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`saves new and existing sources through create or update only`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`requires saving edited supplier facts before syncing the selected source`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`applies discover normalize to the form and keeps suggestions opt-in`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`continues to account sync when discover only has optional suggestions`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`shows discover failure message and failed_step outside the sync-result block`
- `backend/internal/handler/admin/supplier_source_handler_test.go`::`TestUS048_DiscoverUpstreamListFailureReturnsSafeMessageAndFailedStep`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`renders every probe result and actual account change returned by sync`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`does not show success when a resolved sync result reports a failed step`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`shows protocol_unsupported probe results from a 422 response without success wording`
- `frontend/src/views/admin/__tests__/SupplierSourcesView.spec.ts`::`selects the supplier source requested by source_id after loading the list`
- `frontend/src/views/admin/__tests__/AccountsView.supplierManaged.spec.ts`::`shows the badge and disables row and mixed-selection generic writes`
- `frontend/src/components/account/__tests__/SupplierManagedBadge.spec.ts`::`uses marker presence, maps known sources, falls back safely, and refreshes after remount`
- `frontend/e2e/us048-supplier-source-management.e2e.ts`::`US048 Jiajie saves facts previews priority and syncs one band account`
- `frontend/e2e/us048-supplier-source-management.e2e.ts`::`US048 FMGo shows the fixed protocol boundary without account changes`
- `frontend/e2e/us048-supplier-source-management.e2e.ts`::`US048 accounts UI marks supplier-managed accounts and explains read-only ownership`

- Run command:

```bash
cd backend && go test ./internal/service ./internal/repository ./internal/handler/admin ./internal/server/routes -run 'TestUS048|TestSupplierSource|TestModelSupplierSource|TestSchedulingPriorityIgnoresAccountExtra|TestRedactAuditBody_RedactsSupplierSourceCredential' -count=1
cd backend && DOCKER_HOST=unix:///Users/feng/.colima/default/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock go test -tags integration ./internal/repository -run 'TestAccountRepoSuite/TestUS048_Supplier|TestModelSupplierSourceMigrationCreatesOnlyTheSingleSourceTable' -count=1
cd frontend && pnpm exec vitest run src/views/admin/__tests__/SupplierSourcesView.spec.ts src/views/admin/__tests__/AccountsView.supplierManaged.spec.ts src/components/account/__tests__/SupplierManagedBadge.spec.ts
cd frontend && pnpm exec playwright test e2e/us048-supplier-source-management.e2e.ts --project=chromium
```

## Evidence

- 后端、组件和真实 UI 行为由 Linked Tests 生成。
- Playwright Chromium 驱动真实页面，但 API 使用路由 mock；它证明表单、预览、同步结果、失败封闭和账号 ownership UI，不证明真实供应商接入成功。
- 首批三个案例事实不完整或不匹配时保持 `not_run` / `protocol_unsupported` / 只读库存，要求补全准确信息，不以 mock 代替真实上游验收，也不扩大匹配。

## Status

- [x] InTest
