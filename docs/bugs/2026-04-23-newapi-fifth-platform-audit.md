---
title: 第五平台 newapi / OpenAI-compat 调度链路严重 Bug 审计
date: 2026-04-23
auditor: Cloud Agent (Composer)
scope: backend (newapi 第五平台 + OpenAI-compat 调度池 + Bridge dispatch)
status: draft  # 等人工 triage
related_design: docs/approved/newapi-as-fifth-platform.md
upstream_pin: f995a868e4551e3180c7d836561a5a257dae93dc (.new-api-ref)
---

# 概述

本次审计聚焦 TokenKey 第五平台 `newapi`（含 OpenAI-compat 调度池、Bridge dispatch、品牌相关改动）以及与 sub2api upstream 的隔离面。优先级排序遵循 OPC + 乔布斯哲学：

- **P0**：会导致生产请求 5xx / 静默错路由 / panic / 数据持久化错误，必须立刻修。
- **P1**：会让用户感知到错误体验或埋下高回滚成本，应在下一个常规 PR 内修。
- **P2**：风险窗口窄或仅影响诊断 / 错误信息可读性，可批量延后。

**审计原则**：凡属于上游 `sub2api` 共有的逻辑（如 `SelectAccountWithLoadAwareness` 内的 channel pricing 检查），改动方案优先维持「在 TK 侧补一个最小绕道」而不是改动上游高粘性文件；凡仅与 TK 第五平台 / 品牌相关的，按 §5.x「override default」原则处理。

---

## P0-1：`SelectAccountWithScheduler` 完全跳过 channel pricing / 模型限制检查

**位置**：
- `backend/internal/service/openai_account_scheduler.go` 226-291（`Select`）
- `backend/internal/service/openai_account_scheduler.go` 568-752（`selectByLoadBalance`）
- `backend/internal/service/openai_account_scheduler.go` 293-365（`selectBySessionHash`）

**对照基线**：
```1218:1232:backend/internal/service/openai_gateway_service.go
func (s *OpenAIGatewayService) selectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, stickyAccountID int64) (*Account, error) {
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}
```

`SelectAccountWithLoadAwareness`（旧入口）和 `selectAccountForModelWithExclusions` 都会先调 `checkChannelPricingRestriction` 做模型粒度限流，并在 sticky / load-balance 各阶段调 `needsUpstreamChannelRestrictionCheck` + `isUpstreamModelRestrictedByChannel`。

但**新调度入口** `SelectAccountWithScheduler` →  `defaultOpenAIAccountScheduler.Select` **完全没有**任何 channel restriction / pricing 检查：

```bash
$ rg 'channelService|isUpstreamModelRestricted|checkChannelPricingRestriction|needsUpstream' \
    backend/internal/service/openai_account_scheduler.go
# 0 hits
```

**调用方**（直接命中现网热路径）：
- `backend/internal/handler/openai_gateway_handler.go:243, 648, 1171`（Responses）
- `backend/internal/handler/openai_gateway_embeddings_images.go:117, 140, 405, 428`
- `backend/internal/handler/openai_chat_completions.go:121, 144`

**生产影响**（按"用户感知"递减）：
1. 已被运营在 channel 配置中明确「限制模型」的 group，在新调度入口下会**绕过白名单**，把请求路由到本不该承接该模型的账号 → 上游 4xx，但在我方账号 LRU 中已被记成"成功调度"。
2. 渠道定价表中没有的模型，原本应直接 `fmt.Errorf("...channel pricing restriction)")` 拒绝，现在则进 forward → 触发计费层"模型未定价"的兜底分支。
3. `BillingModelSourceUpstream` 模式下，sticky-bound 账号即便上游已对该模型限流，仍会持续被命中。

**为什么是 P0**：白名单/限流是 sub2api 的核心 group/channel 治理面。绕过它等价于"安全边界变更"（按 CLAUDE.md §产品哲学，属于高风险变更类别）。两个调度入口语义不一致还会让运营在排查时彻底失去对账号选择行为的预期。

**修复方向**（优先维持 OPC funnel，不改 upstream 文件）：
- 在 `defaultOpenAIAccountScheduler.Select` 入口处调用 `s.service.checkChannelPricingRestriction`（已有），失败时直接 `return nil, decision, ErrNoAvailableAccounts`。
- 在 `selectByLoadBalance` 候选过滤（line 588-615）和 `selectBySessionHash`（line 320-340）各自加 `s.service.needsUpstreamChannelRestrictionCheck` + `s.service.isUpstreamModelRestrictedByChannel`，与 `SelectAccountWithLoadAwareness` 行为对齐。
- 加 unit test：`TestUS011_Scheduler_RespectsChannelRestriction`。

---

## P0-2：`tryStickySessionHit` 在「sticky 绑定指向跨平台账号」时不清理 Redis 映射

**位置**：`backend/internal/service/openai_gateway_service.go:1314-1318`

```1314:1318:backend/internal/service/openai_gateway_service.go
	// 验证账号是否可用于当前请求
	// Verify account is usable for current request
	if !account.IsSchedulable() || !account.IsOpenAICompatPoolMember(groupPlatform) {
		return nil
	}
```

如果一个 group 之前是 `openai` 平台，sticky 绑定写到 Redis 后管理员把 group 改成 `newapi`（或者反之），这条 sticky 绑定会**整个 TTL 周期内每次请求都重做一次 snapshot 查询 → 命中跨平台账号 → silent return nil → 落到 Layer 2 重新选号**。

对比同一个文件 line 1322-1326（recheck 失败）和 line 1294-1305（账号已删除），这两个分支都会调 `deleteStickySessionAccountID`。**唯独跨平台不匹配的分支没有清理**。

`openai_account_scheduler.go:324` 也存在同样形态——但那里 `IsOpenAICompatPoolMember(req.GroupPlatform)` 不通过时**会**调 `deleteStickySessionAccountID`，行为反而是对的。两个调度路径行为不对称，进一步加深了 P0-1 中的"行为不一致"风险面。

**生产影响**：
- 任何 group 平台变更（含管理员误操作回滚）都会留下"僵尸 sticky"，每次请求多打一次 snapshot/DB 查询 + 一次 Redis miss，在 sessionHash 高频复用的客户端（Codex / Claude Code）下会产生持续的 P99 抖动。
- 与 US-025（账号删除自愈）对称，但 US-025 已修跨平台不匹配未修——是同一个修复批次的遗漏。

**修复方向**：
```go
if !account.IsSchedulable() || !account.IsOpenAICompatPoolMember(groupPlatform) {
    // 与 recheck 失败/账号删除分支对称：跨平台/不可调度都视为"绑定已死",立即清理
    _ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
    return nil
}
```

加 regression test 覆盖：US-025 同形态 + group 平台从 openai → newapi 切换场景。

---

## P0-3：`ResolveMoonshotRegionalBaseAtSave` 错误格式化越界 panic

**位置**：`backend/internal/integration/newapi/moonshot_resolve_save.go:149`

```146:150:backend/internal/integration/newapi/moonshot_resolve_save.go
	if winner != "" {
		return strings.TrimRight(strings.TrimSpace(winner), "/"), nil
	}
	return "", fmt.Errorf("moonshot regional resolve: %v; %v", errs[0], errs[1])
}
```

错误路径硬编码 `errs[0], errs[1]`，但 `bases` 实际长度由 `moonshotProbeBasesForTest` 注入决定（line 121-123）。生产路径下 `len(bases)==2` 没问题，但：

1. 任何后续把 `moonshotOfficialProbeBases` 改成 1 个或 3 个根（例如新增 `api.moonshot.com.cn` 或裁剪掉一个），编译期不会报错，运行时直接 `index out of range` panic。
2. **测试代码已经在用** `moonshotProbeBasesForTest` 注入不同长度，未来如有人写一个 1-base 的失败用例就会直接 panic。
3. Panic 发生在 admin 保存账号的 HTTP 请求线程中，会让 admin UI 收到 500，且 stack trace 会污染日志。

**修复方向**（最小侵入，纯 TK 文件）：
```go
joined := make([]string, 0, len(errs))
for i, e := range errs {
    if e == nil { continue }
    joined = append(joined, fmt.Sprintf("[%s]: %v", bases[i], e))
}
return "", fmt.Errorf("moonshot regional resolve: %s", strings.Join(joined, "; "))
```

附带 regression test：注入 1 个 base 触发失败路径，断言不 panic。

---

## P1-1：`BulkUpdateAccounts` 跳过了 `resolveNewAPIMoonshotBaseURLOnSave`

**位置**：`backend/internal/service/admin_service.go:1789-1903`（`BulkUpdateAccounts`）vs. `1599`、`1764` 单条 Create/Update 路径。

单条 `CreateAccount`、`UpdateAccount` 都会调用 `resolveNewAPIMoonshotBaseURLOnSave` 重新做区域探测，但 `BulkUpdateAccounts` 直接走 `accountRepo.BulkUpdate`，**完全不读 platform / channel_type / credentials 进行 Moonshot 区域校验**。

后果：管理员通过批量编辑界面修改一组 newapi/Moonshot 账号的 `api_key`（这是真实的批量场景，例如轮换密钥），区域绑定不会被重新探测，可能导致：
- 国际站新 key + 库内仍是 `api.moonshot.cn` → 之后所有该账号的 relay 都 401。
- 与单条 Update 行为不一致，复现路径取决于运营从哪个入口操作。

**为什么是 P1（不是 P0）**：批量改 key 的流量在我方运营中并不高频；Bug B 的注释明确说"relay 热路径不做 401 fallback"，所以错的区域是"持续 401"而不是"间歇性错路由"，运营至少能看见错误。

**修复方向**：
- 短期：在 `BulkUpdateAccounts` 入口，如果 `input.Credentials != nil`（即批量改了 base_url 或 api_key），对涉及到的 newapi/Moonshot 账号逐一回退到单条 `UpdateAccount` 路径。
- 长期：让 `resolveNewAPIMoonshotBaseURLOnSave` 接受 `account` 切片，BulkUpdate 在落库前 fan-out 探测。但这需要 `accountRepo.BulkUpdate` 把 base_url 字段挂回 credentials 写入路径，会动到上游共有的 repo 接口，不优先做。

---

## P1-2：`scheduler.selectByLoadBalance` 错误信息硬写 "no available OpenAI accounts"

**位置**：`backend/internal/service/openai_account_scheduler.go:577, 617`

```576:577:backend/internal/service/openai_account_scheduler.go
	if len(accounts) == 0 {
		return nil, 0, 0, 0, errors.New("no available OpenAI accounts")
	}
```

OpenAI-compat 调度池现在同时承载 `openai` 和 `newapi` 两个平台，但错误信息 hard-code 了 "OpenAI"。运营在 newapi group 上看到 `no available OpenAI accounts` 会怀疑系统串平台、白白引发一轮误诊。

**为什么不是 P0**：纯诊断质量问题，不影响行为。

**修复方向**：
```go
return nil, 0, 0, 0, fmt.Errorf("no available accounts for platform %q", req.GroupPlatform)
```

`groupPlatform == ""` 时 fallback 到 `"openai"` 即可。Test 文件 `openai_account_scheduler_tk_newapi_test.go:136-138` 当前用 `Contains` 检查 "no available openai accounts" / "no available accounts" 都能放行，不会破坏。

---

## P1-3：`accountUsesNewAPIAdaptorBridge` kill switch 不区分 endpoint，导致全量回滚

**位置**：`backend/internal/service/gateway_bridge_dispatch.go:45-58`

```45:58:backend/internal/service/gateway_bridge_dispatch.go
func accountUsesNewAPIAdaptorBridge(settings *SettingService, account *Account, endpoint string) bool {
	if account == nil || account.ChannelType <= 0 {
		return false
	}
	if settings != nil && !settings.IsNewAPIBridgeEnabled(context.Background()) {
		return false
	}
	switch endpoint {
	case BridgeEndpointChatCompletions, BridgeEndpointResponses, BridgeEndpointEmbeddings, BridgeEndpointImages:
		return true
	default:
		return false
	}
}
```

kill switch `IsNewAPIBridgeEnabled` 一旦关闭，**所有** newapi (channel_type>0) 账号在 chat/responses/embeddings/images 全部 endpoint 都会 fallback 到 `ForwardAsChatCompletions` / `Forward` / `ForwardAsEmbeddings` / `ForwardAsImageGenerations`——这些入口对 channel_type>0 的"非真正 OpenAI"账号几乎一定会用错 base_url、错 token 形式，立刻 4xx。

也就是说，这个 kill switch 表面上写的是"关掉 bridge 走旧路径"，但实际上对第五平台账号等价于"完全停止服务"。本意应当是"灰度回退某一个 endpoint 出问题时只回退该 endpoint，避免连锁失败"。

**为什么是 P1**：kill switch 当前默认开启，且没有任何 caller 主动关它（`rg SettingKeyNewAPIBridgeEnabled` 全部是测试文件 + 设置 UI），生产风险窗口窄。但运营一旦在出问题时按"先关 bridge 试试"操作，就会扩大事故范围而不是缩小——这是典型的"应急预案变事故放大器"。

**修复方向**：
- 短期（最小改动）：在 `IsNewAPIBridgeEnabled() == false` 且 `account.Platform == PlatformNewAPI` 时，直接 `return false` + 上层让 `Forward*` 知道返回 503（或加一条 `ErrNewAPIBridgeDisabled` 让 handler 渲染明确错误），而不是默默走错路径。
- 中期：把 kill switch 拆成 per-endpoint，例如 `newapi_bridge_enabled.responses=false` 单独关闭 Responses 而不影响 chat completions。

---

## P1-4：`scheduler.selectBySessionHash` `s.service.cache == nil` 守卫与 `selectAccountForModelWithExclusions` 不一致

**位置**：`backend/internal/service/openai_account_scheduler.go:298`

```297:300:backend/internal/service/openai_account_scheduler.go
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, nil
	}
```

scheduler 路径 `cache == nil` 时直接 return nil；旧路径 `tryStickySessionHit` 没有这个守卫，但旧路径会用 `s.cache != nil` 隐式跳过 sticky 写入。差异在于：

- 如果 `cache == nil`（cache 未初始化或本地失败），新调度路径完全跳过 sticky；
- 旧路径会继续走 `getStickySessionAccountID` → 拿到 0 → return nil。

短期不影响，但两个路径对 cache 不可用时的"是否短路"行为不一致，会让 incident response 难以推断"是 cache 死了还是 sticky 没命中"。

**为什么是 P1**：本身不是错误，但与 P0-1、P0-2 同属"两个调度入口语义漂移"系列。建议批量统一。

---

## P2-1：`isOpenAICompatPlatformGroup` 在路由层有第二份定义

**位置**：
- `backend/internal/service/openai_messages_dispatch_tk_newapi.go:23` (`isOpenAICompatPlatformGroup`)
- `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go:16` (`isOpenAICompatPlatform` / 路由层 wrapper)
- `backend/internal/service/account_tk_compat_pool.go:64` (`IsOpenAICompatPlatform` / 服务层 funnel)

服务层已经有 `IsOpenAICompatPlatform`（导出版）作为 single source of truth，但服务层包内又定义了一个 `isOpenAICompatPlatformGroup(g *Group)`，路由层又有一个 `isOpenAICompatPlatform(string)` wrapper。这是 §3 类的"funnel 漂移源"——加第六平台时容易遗漏其中之一。

`scripts/preflight.sh § 9` 的 grep 模式 `!\s*account\.IsOpenAI\(\)` 不能覆盖到 `g.Platform == PlatformOpenAI` 这种 group-level 写法。

**为什么是 P2**：当前没有错，preflight 已经在保护账号侧的回归。group 侧未来加平台时如果忘记更新 `isOpenAICompatPlatformGroup`，单测 `TestUS009_Sanitize_*` 会立即捕获，所以风险窗口窄。

**修复方向**：
- 删除 `isOpenAICompatPlatformGroup`，让 `sanitizeGroupMessagesDispatchFields` 直接调用 `IsOpenAICompatPlatform(g.Platform)`。
- preflight § 9 增加一段：`g.Platform == PlatformOpenAI` 形态在 service 包外的出现都视为 drift。

---

## P2-2：`MaybeResolveMoonshotBaseURLForNewAPI` 在空 api key 时静默跳过

**位置**：`backend/internal/integration/newapi/moonshot_resolve_save.go:51-56`

```51:56:backend/internal/integration/newapi/moonshot_resolve_save.go
	if strings.TrimSpace(apiKey) == "" {
		// Validation of credential completeness is the caller's responsibility;
		// we just skip cold probing rather than fail the save with a confusing
		// "moonshot regional resolve: api key is empty" error.
		return "", false, nil
	}
```

注释承诺 "Validation of credential completeness is the caller's responsibility"，但 `tkValidateNewAPIAccountCreate` 只校验 `base_url` 不校验 `api_key`：

```10:23:backend/internal/handler/admin/account_handler_tk_newapi_validate.go
func tkValidateNewAPIAccountCreate(platform string, channelType int, credentials map[string]any) string {
	if channelType < 0 {
		return "channel_type must be >= 0"
	}
	if platform == domain.PlatformNewAPI {
		if channelType <= 0 {
			return "channel_type must be > 0 for newapi platform"
		}
		if baseURL, _ := credentials["base_url"].(string); strings.TrimSpace(baseURL) == "" {
			return "credentials.base_url is required for newapi platform"
		}
	}
	return ""
}
```

→ 管理员可以创建一个 api_key 为空的 Moonshot newapi 账号，区域探测被静默跳过，`base_url` 用默认值落库，账号永远 401。

**为什么是 P2**：admin UI 大概率会要求填 api_key（前端校验），但没有后端兜底。

**修复方向**：在 `tkValidateNewAPIAccountCreate` 加一行 `api_key` 必填校验。Update 路径同步。

---

## P2-3：`embedding_relay.go` 不支持 PassThrough，但 `text_relay.go` / `responses_relay.go` 支持

**位置**：`backend/internal/relay/bridge/embedding_relay.go`（无 PassThrough 分支）

`text_relay.go:73` 和 `responses_relay.go:75` 都有：
```go
if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
    storage, err := common.GetBodyStorage(c)
    ...
}
```

`embedding_relay.go` 缺这个分支，所以 newapi 账号上启用了 PassThrough 的 embedding 请求会强制走 `adaptor.ConvertEmbeddingRequest` 转换，可能丢失客户端自定义字段。

**为什么是 P2**：embedding 走 PassThrough 是少见配置，且我方目前没有 newapi/embedding 的活跃运营。但这是 sub2api 上游变更时容易遗漏的隐藏面。

---

## P2-4：`ChannelType` 索引检查使用 `len(ChannelBaseURLs)` 但允许越界值

**位置**：`backend/internal/integration/newapi/channel_types.go:34`

```33:36:backend/internal/integration/newapi/channel_types.go
		var baseURL string
		if channelType >= 0 && channelType < len(newapiconstant.ChannelBaseURLs) {
			baseURL = newapiconstant.ChannelBaseURLs[channelType]
		}
```

依赖上游 `ChannelBaseURLs` slice 长度。如果 upstream 重排 / 新增/删除 channel type 但忘记同步 `ChannelBaseURLs`，TK 会静默给前端返回空 `BaseURL` 而不是上游正确的默认根。`scripts/check-newapi-sentinels.py` 当前不覆盖这种 slice 形状漂移。

**为什么是 P2**：upstream 已经维护这个 slice 很久，长度变化一旦发生 grep / 测试都会报。但属于"upstream merge 时静默断裂"高风险类，sentinel 应当扩展。

**修复方向**：在 `scripts/newapi-sentinels.json` 增加一条针对 `ChannelBaseURLs` 长度的 sentinel；或者直接调上游 helper 而不是裸索引。

---

## 跨 bug 共通模式

- **funnel 漂移**：`SelectAccountWithLoadAwareness` 与 `SelectAccountWithScheduler` 两个调度入口在 channel restriction、sticky cleanup、cache nil 守卫上行为不一致 → P0-1 / P0-2 / P1-4 都属于这一类。建议统一由 `defaultOpenAIAccountScheduler` 调用 `OpenAIGatewayService` 的同一组 helper（preflight § 9 的现有 grep 不足以拦截这种"调用图缺失"型漂移）。
- **save-time 探测漂移**：`CreateAccount` / `UpdateAccount` 都接入了 `resolveNewAPIMoonshotBaseURLOnSave`，但 `BulkUpdateAccounts` 没有 → P1-1。所有"账号写入路径"都应该走同一个 funnel。建议把"凡是改 credentials 的 admin 写入"封装成一个 `accountSaveHook`。
- **错误信息品牌漂移**：`no available OpenAI accounts`（P1-2）、kill switch 名字（P1-3）等还在用 OpenAI 字样。这是品牌（TokenKey）和上游（sub2api）混在一起的可见面，建议在 §6 之后做一轮文本审计。
- **空切片越界**：`ResolveMoonshotRegionalBaseAtSave`（P0-3）、`tkValidateNewAPIAccountCreate` 不校验 api_key（P2-2）都属于"接口契约和实现不对齐"类，应在 contract 层（`scripts/export_agent_contract.py`）加一条断言。

## 不在本审计范围

- 第五平台之外的 sub2api 上游路径（如 Anthropic / Gemini / Antigravity 单平台）。
- 前端 / Vue 侧改动。
- 计费层 / 配额层。
- Cloud agent / CI 工作流。
