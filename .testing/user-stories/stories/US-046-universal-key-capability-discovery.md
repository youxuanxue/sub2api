# US-046-universal-key-capability-discovery

- ID: US-046
- Title: 默认 API Key 的诚实能力发现与统一菜单
- Priority: P0
- As a / I want / So that: 作为使用同一把默认 API Key 的开发者，我希望网站和每个协议客户端只列出该 key 在该协议下真正可调用的模型，从而无需猜测模型、协议和落地服务是否匹配。
- Trace: `docs/approved/universal-key-capability-discovery.md`
- Risk Focus:
  - 逻辑错误：候选模型没有按协议形状和账号真实能力过滤，或选择结果与运行时 resolver 不一致。
  - 行为回归：direct key、OpenAI/Anthropic/Gemini/Codex/Antigravity 原生 schema 或现有请求调度发生变化。
  - 安全问题：站内 capability API 越权读取其他用户的 key，或标准端点泄露内部组信息。
  - 运行时问题：授权、数据库或 provider 失败被吞掉并返回 200 空列表。

## Acceptance Criteria

1. AC-001 (正向): Given 自动路由 key 拥有多个授权组，When capability owner 按协议计算模型，Then 只返回至少一个授权组可服务该协议形状且 resolver 能确定选择的模型，并返回稳定排序结果。
2. AC-002 (负向): Given 候选模型只支持另一端点形状或没有可调度账号，When 请求协议发现列表，Then 该模型不出现在响应中。
3. AC-003 (错误): Given 授权组或能力 provider 返回内部错误，When 请求任意发现入口，Then 返回 5xx，且不会返回成功空列表。
4. AC-004 (契约): Given 同一把自动路由 key，When 分别请求 OpenAI/Anthropic、Gemini、Codex 与 Antigravity 发现入口，Then 每个入口返回其原生 schema 与该协议可调用子集。
5. AC-005 (安全): Given 已登录用户请求另一个用户的 key capability，When 调用站内 capability API，Then 返回 404 且不泄露目标 key 元数据。
6. AC-006 (UI): Given 用户打开 Quickstart 或 Studio 并选择自动路由 key，When 页面加载菜单，Then 浏览器只请求 capability SSOT 并按协议/模态展示；加载失败显示错误而不是空菜单。
7. AC-007 (回归): Given direct key 与已有实际推理请求，When 执行回归测试，Then direct key 仍限制到绑定组，实际请求的解析、计费与调度行为不变。
8. AC-008 (站内价目): Given 用户选择自己的自动路由 key，When 请求 `me/pricing-catalog?api_key_id=`，Then 返回用户授权组价目的稳定并集、`target_group: null` 与逐模型授权组索引；未知或其他用户的 key 仍返回 404。

## Assertions

- 响应中的每个模型都有对应 shape 的 `UniversalGroupSupportsRequest == true`，并存在确定性选中组。
- 不支持当前协议的跨模态模型不会进入该协议响应。
- 标准响应不含 `group_id`、倍率或授权组集合。
- capability API 同时校验 JWT user ID 与 key ownership。
- 内部错误和业务空集使用不同状态码。

## Linked Tests

- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_CapabilitiesOnlyListCallableModelsForProtocol`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_CapabilitiesPropagateEntitlementFailure`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_CapabilitiesPropagateAvailabilityFailure`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_CapabilitiesUnionMappedAndPassthroughCandidates`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_DirectKeyCapabilitiesStayGroupBound`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_DirectKeyCapabilitiesRespectProtocolPlatform`
- `backend/internal/service/us046_universal_capability_test.go`::`TestUS046_CapabilitiesRejectUnsupportedUnhintedModel`
- `backend/internal/handler/us046_universal_discovery_test.go`::`TestUS046_DiscoveryEndpointsUseNativeSchemas`
- `backend/internal/handler/us046_universal_discovery_test.go`::`TestUS046_UniversalDiscoveryFailureIs500NotEmptyList`
- `backend/internal/handler/us046_universal_discovery_test.go`::`TestUS046_CapabilityEndpointRejectsForeignKey`
- `backend/internal/handler/us046_universal_discovery_test.go`::`TestUS046_CodexDiscoveryPathsUseAuthorizedOpenAIGroup`
- `backend/internal/handler/us046_universal_discovery_test.go`::`TestUS046_CodexDiscoveryReturnsNativeEmptyManifestWithoutCapability`
- `backend/internal/service/me_pricing_catalog_tk_test.go`::`TestUS046_MePricingCatalog_UniversalKeyReturnsUserAuthorizationUnion`
- `backend/internal/handler/me_pricing_catalog_handler_tk_test.go`::`TestUS046_MePricingHandler_UniversalScopeSerializesNullTargetGroup`
- `frontend/src/views/__tests__/PricingView.spec.ts`::`keeps automatic-routing keys selectable and labels their user-wide scope`
- `frontend/src/composables/__tests__/useTkUseKey.spec.ts`::`uses only the key capability SSOT for automatic routing and filters by protocol metadata`
- `frontend/src/views/user/studio/__tests__/MediaStudioView.spec.ts`::`uses capability modalities instead of public pricing to select and scope automatic keys`
- `frontend/e2e/us046-universal-capability-discovery.e2e.ts`::`US046 automatic routing key uses the capability menu`

- Run command:

```bash
cd backend && go test ./internal/service ./internal/handler -run 'TestUS046' -count=1
cd frontend && pnpm exec vitest run src/composables/__tests__/useTkUseKey.spec.ts src/views/user/studio/__tests__/MediaStudioView.spec.ts
cd frontend && pnpm exec playwright test e2e/us046-universal-capability-discovery.e2e.ts --project=chromium
```

## Evidence

- Automated evidence is produced by the linked test commands and PR CI.
- Runtime UI evidence is the Playwright trace/screenshot produced by the linked e2e test on failure.

## Status

- [x] Done
