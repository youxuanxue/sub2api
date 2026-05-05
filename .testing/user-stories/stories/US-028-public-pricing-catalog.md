# US-028-public-pricing-catalog

- ID: US-028
- Title: Public model + pricing catalog reachable without auth so visitors can decide before registering
- Version: V1.5 (cold-start P0-A)
- Priority: P0
- As a / I want / So that:
  作为 **未登录的潜在 TokenKey 用户**，我希望 **进站点首页就能看到「这个站支持哪些模型 + 单价 + 上下文窗口 + 走哪条协议」的目录页**，**以便** 我在不消耗试用额度（甚至不留邮箱）的前提下，自己判断 TokenKey 是否覆盖我的工作流；不再像 L 站 `t/topic/1413702` 反映的那样，「点进去只见登录页就走了」。

- Trace:
  - 角色 × 能力轴线：游客 × 「了解站点支持范围」——今天该格是空的，没有任何公开入口暴露模型 / 价格 / 协议信息。
  - 防御需求轴线：公开端点必须只暴露元数据，**不得**带出 account / channel_type / api_key / access_token 等敏感字段。
  - 系统事件轴线：每次未登录访问 `/pricing` 页面 → 调用 `GET /api/v1/public/pricing` → 响应应在 60s 缓存窗口内可重复获取，避免对站点形成爬虫源头。

- Risk Focus:
  - 逻辑错误：响应 `data[]` 必须基于已加载的 `pricingData` 字典生成；空字典或 PricingService 未初始化时返回空集合而非 500。每条 entry 必须有可识别的 `model_id` + 价格字段，缺价格的条目不应出现。
  - 行为回归：与现有 `GET /v1/models`（鉴权后端点）行为正交，本端点不得改 `/v1/models` 任何行为；现有 `model-pricing` JSON 加载逻辑不得被改坏。
  - 安全问题：响应 JSON 中**不得**出现 `account_id`、`channel_type`、`api_key`、`access_token`、`organization`、`base_url`、内部 `cost_per_token` 浮点原值（必须按 1k token 单价输出）。
  - 不适用：状态机——本端点是只读元数据查询，无状态迁移。

> **v1 范围说明（per design §2 v1 deferred）**：PR 1 只 ship 扁平的 `data: [{model_id, vendor, pricing, context_window, max_output_tokens, capabilities}]`，不含 `groups[]` 反查 / `endpoints[]` / `vendors[]` 聚合 / `pricing_catalog_groups_visible` 过滤 / `?group_id=` query。这些在 follow-up PR 落（需要 `visible_in_catalog` schema 字段 + group → model 反向聚合，属于独立工程范围）。

## Acceptance Criteria

1. **AC-001 (正向 / 未登录可访)**：Given setting `pricing_catalog_public=true` 且 `PricingService` 已加载至少一条 model pricing，When 未带 `Authorization` 头 GET `/api/v1/public/pricing`，Then 返回 200，body `object == "list"`，`data[]` 非空，含 `currency` 与 `updated_at` 顶层字段。
2. **AC-002 (正向 / 字段形状)**：Given 上述请求，When 取 `data[0]`，Then 至少包含 `model_id`（string，非空）、`pricing.currency == "USD"`、`pricing.input_per_1k_tokens`（number，>= 0）、`pricing.output_per_1k_tokens`（number，>= 0）；可选 `vendor`、`context_window`、`max_output_tokens`、`capabilities[]` 在源数据具备时透出。
3. **AC-003 (负向 / setting 关闭 → 404)**：Given setting `pricing_catalog_public=false`，When GET `/api/v1/public/pricing`，Then 返回 404，响应 body 不含 `"object":"list"`（不得用 200 + 空 body 暗示路由存在）。
4. **AC-004 (负向 / 安全字段不泄漏)**：Given 任意配置，When 解析响应 JSON 字符串，Then **不**包含子串 `account_id`、`channel_type`、`api_key`、`access_token`、`organization`、`base_url`、`cost_per_token`（精确字符串匹配；防御性，确保未来新加字段时审视一次）。
5. **AC-005 (负向 / PricingService 未加载 → 空集合不 500)**：Given `PricingService.pricingData` 为空（启动期 / 加载失败 fallback），When GET `/api/v1/public/pricing`，Then 返回 200，`data == []`，不抛 500。
6. **AC-006 (回归 / 鉴权 `/v1/models` 行为不变)**：Given 本 PR 落地，When 持有效 API Key GET `/v1/models`，Then 响应结构、字段、模型集合与 baseline 完全一致（沿用现有 `TestGetAvailableModels_*`）。
7. **AC-007 (回归 / 单元测试全绿)**：Given 本 PR 落地，When 执行 `go test -tags=unit -count=1 ./internal/...`，Then 全部包通过。

## Assertions

- `httpGet("/api/v1/public/pricing")` → status=200，`json.object == "list"`，`json.data` 长度 > 0（fixture 注入至少 1 条 pricing entry）。
- `httpGet("/api/v1/public/pricing")` 响应中 `data[0].pricing.currency == "USD"`，`data[0].pricing.input_per_1k_tokens` 与 `output_per_1k_tokens` 均为 `>= 0` 的 number。
- 在 setting `pricing_catalog_public=false` 时，`httpGet("/api/v1/public/pricing")` → status=404，body 不含 `"object":"list"`。
- 序列化后的响应字符串对 7 个敏感子串（`account_id`、`channel_type`、`api_key`、`access_token`、`organization`、`base_url`、`cost_per_token`）逐一 `strings.Contains == false`。
- 用空 `pricingData` 构造 PricingService 并请求 → status=200，`json.data == []`（不抛 500）。

## Linked Tests

- `backend/internal/handler/us028_public_pricing_catalog_test.go`::`TestUS028_UnauthReturnsListShape`
- `backend/internal/handler/us028_public_pricing_catalog_test.go`::`TestUS028_EntryFieldsHaveExpectedShape`
- `backend/internal/handler/us028_public_pricing_catalog_test.go`::`TestUS028_DisabledBySetting404`
- `backend/internal/handler/us028_public_pricing_catalog_test.go`::`TestUS028_NoSensitiveFieldsInPayload`
- `backend/internal/handler/us028_public_pricing_catalog_test.go`::`TestUS028_EmptyCatalogReturnsEmptyList`
- `backend/internal/service/pricing_catalog_tk_test.go`::`TestPricingCatalogService_ParsesLiteLLMShape`
- `backend/internal/service/pricing_catalog_tk_test.go`::`TestPricingCatalogService_EmptyOrUnparseableSourceReturnsEmptyList`
- `backend/internal/service/pricing_catalog_tk_test.go`::`TestPricingCatalogService_CachesByMTime`
- `backend/internal/service/pricing_catalog_tk_test.go`::`TestPricingCatalogService_NilReceiverIsSafe`

运行命令：

```bash
# Handler-level (5 tests, 1:1 with AC-001..AC-005)
go test -tags=unit -count=1 -v -run 'TestUS028_' ./internal/handler/...
# Service-level parser + mtime cache coverage
go test -tags=unit -count=1 -v -run 'TestPricingCatalogService_' ./internal/service/...
```

## Evidence

- 不再归档响应快照；以 pricing endpoint 测试和 CI/preflight 输出为准。

## Status

- [x] InTest — handler + service tests landed and green (9 tests across two files); awaiting frontend bundle (Step 4) before flipping to Done.
