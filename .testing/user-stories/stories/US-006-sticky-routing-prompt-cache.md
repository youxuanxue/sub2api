# US-006-sticky-routing-prompt-cache

- ID: US-006
- Title: Upstream prompt-cache 粘性路由（统一注入）
- Version: V1.0
- Priority: P0
- As a / I want / So that: 作为 TokenKey 网关运营方，我希望来自同一逻辑会话的请求能携带稳定的 sticky 标识送达上游，以便最大化 OpenAI/Anthropic/GLM 的 prompt cache 命中率，把长程 Agent 任务的 token 成本降下来。
- Trace: 角色×能力（运营×成本控制）+ 系统事件（每条 gateway 转发请求）+ 防御需求（避免跨 api_key 缓存串桶）
- Risk Focus:
  - 逻辑错误：派生算法稳定性（同输入同输出）；优先级回退（header > metadata.user_id > prompt_cache_key > derived hash）
  - 行为回归：客户端已送 prompt_cache_key 时不被覆盖；既有 compat 路径自动注入仍可用
  - 安全问题：跨 api_key 不互踩；hash 不泄露 system 内容；real Claude Code 客户端的 metadata.user_id 不被改写
  - 运行时问题：strategy 计算热路径性能（每请求 1 次）；并发 derive 一致

## Acceptance Criteria

1. AC-001 (正向 / OpenAI Codex)：Given 客户端连续 5 个请求未送 `prompt_cache_key` 且 system prompt 一致，When 网关派生注入，Then 5 个请求注入相同的 `tk_*` 16-hex 键（`TestUS201_DeriveStable_SameSystemSameKey`）。
2. AC-002 (正向 / Anthropic mimic)：Given 非 Claude Code UA + Anthropic OAuth 账号 + 客户端无 `metadata.user_id`，When 网关注入，Then body.metadata.user_id 被写入派生键（`TestUS201_InjectAnthropicMessages_WhenAllowed`）。
3. AC-003 (负向 / 真 Claude Code)：Given UA = Claude Code + Anthropic OAuth，When 网关处理，Then `metadata.user_id` 不被改写（`TestUS201_InjectAnthropicMessages_SkipsRealClaudeCode`）。
4. AC-004 (负向 / group=off)：Given 分组 `sticky_routing_mode=off`，When 网关处理，Then 不论客户端送什么都不派生不注入（`TestUS201_StrategyOffSkipsEverything`）。
5. AC-005 (负向 / global=off)：Given 全局 `gateway.sticky_routing.enabled=false`，When 网关处理，Then auto 退化为 passthrough：客户端送的键继续转发，但不派生（`TestUS201_GlobalOffForcesPassthrough`）。
6. AC-006 (回归 / 客户端键优先)：Given 客户端 body 已带 `prompt_cache_key`，When 网关派生，Then 客户端值不被覆盖（`TestUS201_DerivePrefersClientPromptCacheKey` + `TestUS201_InjectOpenAI_DoesNotOverrideExisting`）。
7. AC-007 (安全 / 不串桶)：Given 两个不同 `api_key_id` + 完全相同 system + tools，When 派生，Then 两个 key 不相同（`TestUS201_NoCrossAPIKeyCollision`）。
8. AC-008 (安全 / 不可逆)：Given system prompt 含敏感字符串，When 派生，Then 输出 16-hex 键不含原文任何子串（`TestUS201_HashNotReversible`）。

## Assertions

- 派生键格式 = `tk_` + 16 个小写 hex 字符（xxHash64）
- AC-002 后：`gjson.GetBytes(out, "metadata.user_id").String() == key.Value`
- AC-003 后：`gjson.GetBytes(out, "metadata.user_id").Exists() == false`
- AC-005 后：`StickyStrategy{GlobalEnabled:false, Mode:auto}.EffectiveMode() == passthrough`
- AC-006 后：注入函数返回 `(body 不变, mut=false, err=nil)`
- AC-007 后：`derive(api_key=1, body) != derive(api_key=2, body)`
- 失败时 testify `require` 立即终止，非 0 退出码

## Linked Tests

- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_DeriveStable_SameSystemSameKey`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_DeriveStable_DifferentSystemDifferentKey`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_DerivePrefersClientPromptCacheKey`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_DerivePrefersHeaderSessionID`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_DeriveExtractsSessionIDFromMetadata`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_StrategyOffSkipsEverything`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_GlobalOffForcesPassthrough`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_InjectAnthropicMessages_WhenAllowed`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_InjectAnthropicMessages_SkipsRealClaudeCode`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_NoCrossAPIKeyCollision`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_HashNotReversible`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_InjectOpenAI_DoesNotOverrideExisting`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_PassthroughDoesNotDerive`
- `backend/internal/service/sticky_session_injector_test.go`::`TestUS201_InjectXSessionIDHeader`
- 运行命令: `cd backend && go test -tags=unit -v -run 'TestUS201_' ./internal/service/`

## Evidence

- `.testing/user-stories/attachments/us006-sticky-routing-run.txt`（实施完成后归档全量 `go test -v` 输出）

## Status

- [x] InTest（注入器骨架 + 派生算法 + 13 个测试已落盘并通过；P1 wire-in 待并入主线后切 Done）
