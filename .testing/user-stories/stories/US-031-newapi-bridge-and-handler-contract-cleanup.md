# US-031-newapi-bridge-and-handler-contract-cleanup

- ID: US-031
- Title: NewAPI bridge & handler 契约清理 — sticky 注入器拆分 / 双写防护 / TTL 漏斗 / selection-failure 显式 return
- Version: V1.4.x (hot-fix)
- Priority: P2 (B-6) + P3 (B-2 / B-8 / B-10)
- As a / I want / So that:
  作为 **TokenKey 维护者**，我希望 **bridge 错误响应职责、sticky 注入器协议形态、sticky TTL 来源、selection-failure 错误归因这四处隐性契约都转为机械可检查的运行时/源码层约束**，**以便** 任何未来 PR 误违反约定都在 unit-test 阶段被打回，而不是 silently 在生产上引发不一致行为。

- Trace:
  - 防御需求轴线：
    - B-2：Anthropic bridge dispatch 在 service 层自己写 ClaudeError body；OpenAI / embeddings / images bridge 留给 handler 层 `TkTryWriteNewAPIRelayErrorJSON` 写。这是隐性契约——任何人加一行"为统一性"调用 helper 立刻 double-write。
    - B-6：sticky 注入器历史上把 `/v1/chat/completions` 与 `/v1/responses` 混用同一个 `InjectOpenAIChatCompletionsBody`；今天两者实现等价但任何 protocol drift 立即让其中一个静默错配。
  - 角色 × 能力轴线：
    - B-8：`BindStickySession` 内联读 cfg → TTL，与 `openAIWSSessionStickyTTL()` helper 平行，未来 TTL 来源变更需要双改。
    - B-10：`Messages` / `ChatCompletions` selection-failure 分支有冗余嵌套 `if err != nil`，会导致 fall-through 把上游 err 错误归因为 "No available accounts"。

- Risk Focus:
  - 逻辑错误：B-2 guard 必须在 `c.Writer.Size() != writerSizeBeforeForward || streamStarted || c.Writer.Written()` 任一为真时跳过 c.JSON 写入但仍 return true（stop caller）；B-6 拆分必须保证两个 helper 被调用对应的 dispatch entry（chat→Chat, responses→Responses）；B-8 必须把 `cfg.StickySessionTTLSeconds` 路径与默认 fallback 都走 helper；B-10 每个错误分支必须显式 return（不能 fall through 到 `selection==nil` 检查）。
  - 行为回归：所有 4 项修复对成功路径完全无影响；既有 `TestUS201_*`（sticky）/ `TestUS028_*`（B-11 status fallback）/ `TestOpenAISelectAccountWithLoadAwareness_*` / `TestOpsRetry_*` 测试必须全绿。
  - 安全问题：B-6 拆分不引入新代码路径；B-2 guard 不读 body 内容只比较 writer size；B-8 不改 TTL 数值（仅 funnel）；B-10 错误分支只移动 return 位置不改业务逻辑。
  - 运行时问题：B-6 拆分用 function pointer dispatch，无 perf 影响；B-2 guard 增加 1 次 writer state check；B-8 funnel 增加 1 次方法调用；B-10 重构后 cyclomatic complexity 略降。

## Acceptance Criteria

1. **AC-001 (B-6 正向 / chat)**：Given chat-shape body + 启用 sticky strategy + 健康账号，When `applyStickyToNewAPIChatBridge` 调用，Then body root 写入 `prompt_cache_key`，request header 同步 `X-Session-Id`。
2. **AC-002 (B-6 正向 / responses)**：同 AC-001 但用 `applyStickyToNewAPIResponsesBridge` + responses-shape body，行为对称。
3. **AC-003 (B-6 回归 / OffStrategy)**：strategy=Off 时两个 helper 都返回原 body 不写 header。
4. **AC-004 (B-2 正向 / 已写)**：Given service 层已用 `c.JSON(502, ClaudeError)` 写出响应，When handler 调用 `TkTryWriteNewAPIRelayErrorJSON(c, NewAPIRelayError, false, sizeBefore)`，Then 不写第二次 body（status / body 长度保持），但仍 return true。
5. **AC-005 (B-2 回归 / 未写)**：service 层未写时（OpenAI/embeddings/images 路径），helper 仍按原契约写出 OpenAI-shape JSON error。
6. **AC-006 (B-8 funnel / 默认)**：`OpenAIGatewayService{cfg: &Config{}}.BindStickySession()` 触发的 cache.SetSessionAccountID 收到 `ttl == openaiStickySessionTTL`（即默认值，等同 `openAIWSSessionStickyTTL()` 默认返回）。
7. **AC-007 (B-8 funnel / 自定义)**：cfg `StickySessionTTLSeconds = 600` 时 cache 收到 `ttl == 600s`，且 `svc.openAIWSSessionStickyTTL() == 600s`（同源验证）。
8. **AC-008 (B-8 防御)**：sessionHash 空 / accountID <= 0 时 BindStickySession no-op，cache 不被调用。
9. **AC-009 (B-10 OPC 静态守护)**：源代码扫描断言 `openai_gateway_handler.go::Messages` 选号失败块仅含 1 个 `if err != nil`（不是 2，去除冗余嵌套）。
10. **AC-010 (B-10 OPC)**：源代码扫描断言 `openai_chat_completions.go` 选号失败区域 ≤ 2 个 `if err != nil`（外层 + 真正的 post-fallback 检查，不是 3 层嵌套）。
11. **AC-011 (B-10 OPC)**：`Messages` 选号失败区域含 ≥ 2 个显式 `return\n`（每个分支都 explicit return，不 fall through）。
12. **AC-012 (回归 / 全量单元测试)**：`go test -tags=unit -count=1 ./internal/service/... ./internal/handler/...` 全绿。

## Assertions

- `TestUS201_ApplyStickyToNewAPIChatBridge_InjectsBodyAndHeader`: 写入 prompt_cache_key + X-Session-Id 同步
- `TestUS201_ApplyStickyToNewAPIResponsesBridge_InjectsBodyAndHeader`: 对称
- `TestUS201_ApplyStickyToNewAPIChatBridge_OffStrategyNoOp` / `_ResponsesBridge_OffStrategyNoOp`: OffStrategy 时无副作用
- `TestUS031_TkWriteRelayErrorJSON_SkipsWriteWhenServiceLayerAlreadyWrote`: w.Code 保持 502, w.Body.Len 不增长，return true
- `TestUS031_TkWriteRelayErrorJSON_WritesWhenNothingPreWritten`: w.Code == 401, w.Body.Len > 0
- `TestUS031_BindStickySession_UsesOpenAIWSSessionStickyTTL_DefaultPath`: `cache.lastTTL == openaiStickySessionTTL`
- `TestUS031_BindStickySession_UsesOpenAIWSSessionStickyTTL_CustomCfg`: `cache.lastTTL == 600s`
- `TestUS031_BindStickySession_NoOp_OnEmptyInputs`: `cache.calls == 0`
- `TestUS031_OpenAIMessages_SelectionFailureBranch_NoNestedRedundantIfErr`: count ≤ 1
- `TestUS031_OpenAIChatCompletions_SelectionFailureBranch_NoNestedRedundantIfErr`: count ≤ 2
- `TestUS031_OpenAIMessages_SelectionFailureBranch_HasExplicitReturnPerBranch`: returns ≥ 2

## Linked Tests

- `backend/internal/service/sticky_session_context_test.go`::`TestUS201_ApplyStickyToNewAPI{Chat,Responses}Bridge_*` (AC-001 ~ AC-003, B-6)
- `backend/internal/handler/tk_newapi_relay_error_double_write_guard_test.go`::`TestUS031_TkWriteRelayErrorJSON_*` (AC-004 / AC-005, B-2)
- `backend/internal/service/us031_bind_sticky_session_ttl_test.go`::`TestUS031_BindStickySession_*` (AC-006 ~ AC-008, B-8)
- `backend/internal/handler/us031_selection_failure_branch_shape_test.go`::`TestUS031_OpenAI{Messages,ChatCompletions}_SelectionFailureBranch_*` (AC-009 ~ AC-011, B-10)

运行命令：

```bash
cd backend
go test -tags=unit -count=1 ./internal/service/... -run 'TestUS201|TestUS031'
go test -tags=unit -count=1 ./internal/handler/... -run 'TestUS031'
```

## Evidence

- 修复落地：PR-B (branch `cursor/bug-3f0f-prb`) commits:
  - `d42cec7b` — `fix(newapi): split sticky bridge injector for chat vs responses paths (Bug B-6)`
  - `98668c6a` — `fix(newapi): TkTryWriteNewAPIRelayErrorJSON guards against service-layer double-write (Bug B-2)`（commit body 同时记录 B-8）
  - `799174ec` — `fix(newapi): explicit per-branch return on selection failure (Bug B-10)`
  - `32d8307b` — `test(newapi): cover Bug B-8 + B-10 with unit tests (PR-B follow-up)`
- Bug audit 文档：`docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md` § B-2 / § B-6 / § B-8 / § B-10

## Status

- [x] InTest（PR-B 待手工创建 PR；测试已全绿）
