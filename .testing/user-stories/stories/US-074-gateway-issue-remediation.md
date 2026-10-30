# US-074-gateway-issue-remediation

- ID: US-074
- Priority: P1
- Title: 网关保留请求协议语义与最终计量
- As a / I want / So that: 作为 TokenKey API 用户，我希望协议转换不吞正文、工具或输出约束，最终账单采用上游明确的计量修正，使请求结果和费用符合调用意图。
- Trace: `../../../docs/approved/gateway-issue-remediation.md`
- Risk Focus:
  - 逻辑错误：显式零值修正、cache aliases、响应 schema 与工具时序。
  - 行为回归：跨块正文和流式/非流式路径一致，现有工具参数清理不变。
  - 安全问题：真实 reasoning 不泄露；用户正文与项目规则不被身份清理误删；非法动态工具在上游调用前拒绝。
  - 运行时：跨 SSE 块解析、工具结果顺序、有界兼容重试；schema 引用循环和展开膨胀不能拖垮网关；不新增持久状态或后台任务。

## Acceptance Criteria

1. AC-001 (positive): Given 最终 SSE 明确 input/cache=0 When 合并 usage Then 覆盖旧值并清除相矛盾的 TTL 费用；缺失及非法值保留。
2. AC-002 (negative): Given 正文中的标签及真正开头 reasoning When 逐块解码 Then 正文逐字保留且 reasoning 不泄露；裸起始标签歧义继续保守隐藏。
3. AC-003 (regression): Given Gemini SDK response schema aliases 和系统 SDK opener When 转换到 AG wire Then 使用 responseSchema、不发明响应字段，且保留工具参数既有清理和用户指令。
4. AC-004 (negative): Given 动态工具增删/引用 When 转换到实际供应协议 Then 保持有序 additions，无法无损表示或引用不明时拒绝，不能静默丢弃。

5. AC-005 (regression): Given Chat 缓存提示 When 供应不支持并明确拒绝 Then 按共享预算重试且 key 不变；支持者保留 retention，取消和截断错误体不重试。

6. AC-006 (negative): Given schema 自引用、互引用或展开超预算 When 进入 AG 工具/响应 schema 清洗 Then 返回参数错误且上游零调用；无环重复引用保持原有约束与正常转发。

## Assertions

AC-001 断言最终 token 数与 cache creation cost；AC-002 断言流式/非流式正文逐字相等；AC-003 断言完整 schema 与 system/contents 对象；AC-004 断言有序工具输入和错误状态、上游调用次数；AC-005 断言最终出站缓存字段、相同 session identity 与有界请求次数；AC-006 断言错误类型、HTTP 400、上游零调用及正常无环引用的展开结果。

## Linked Tests

- `backend/internal/service/gateway_sse_usage_explicit_zero_tk_test.go`::`TestAnthropicSSEFinalInputCounters` — AC-001。
- `backend/internal/integration/kiro/thinking_redact_test.go`::`TestInlineThinkingRedactor_PreservesAnswerLiteralsAcrossChunks` — AC-002。
- `backend/internal/service/kiro_sse_encoder_tk_thinking_redact_test.go`::`TestKiroGatewayService_Forward_PreservesThinkingLiterals` — AC-002。
- `backend/internal/service/antigravity_gateway_issue_contract_tk_test.go`::`TestCleanGeminiRequest_ResponseSchemaAliases` — AC-003。
- `backend/internal/service/antigravity_gateway_issue_contract_tk_test.go`::`TestAntigravityGeminiWire_NeutralizesSDKIdentity` — AC-003。

- `backend/internal/pkg/apicompat/anthropic_dynamic_tools_test.go`::`TestAnthropicDynamicTools_PreservesOrderAndReferences` — AC-004。
- `backend/internal/pkg/apicompat/anthropic_dynamic_tools_test.go`::`TestAnthropicDynamicTools_RejectsUnrepresentableChanges` — AC-004。

- `backend/internal/service/openai_gateway_dynamic_tools_tk_test.go`::`TestForwardAsAnthropic_DynamicToolErrorsReturn400BeforeUpstream` — AC-004。
- `backend/internal/service/openai_chat_prompt_cache_tk_test.go`::`TestChatResponsesRetriesRejectedExplicitCacheHints` — AC-005。

- `backend/internal/pkg/antigravity/schema_expansion_safety_tk_test.go`::`TestCleanJSONSchemaRejectsUnsafeExpansion` — AC-006。
- `backend/internal/pkg/antigravity/schema_expansion_safety_tk_test.go`::`TestCleanJSONSchemaSharedReferencesRemainIndependent` — AC-006。
- `backend/internal/service/antigravity_gateway_schema_safety_tk_test.go`::`TestAntigravityGatewayRejectsUnsafeSchemasBeforeUpstream` — AC-006。

- 运行命令: `cd backend && GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/integration/kiro ./internal/pkg/apicompat ./internal/pkg/antigravity ./internal/service -run 'TestAnthropicSSEFinal|TestInlineThinking|TestKiroGatewayService_Forward_.*(Thinking|Reasoning)|TestCleanGeminiRequest|TestAntigravityGeminiWire|TestAnthropicToResponses|TestAnthropicDynamicTools|TestCodexDynamicTools|TestForwardAsAnthropic_Dynamic|TestChatResponses|TestOpenAIPromptCacheHints|TestCleanJSONSchema|TestTransformClaudeToGeminiRejectsUnsafeSchema|TestAntigravityGatewayRejectsUnsafeSchemasBeforeUpstream' -count=1`

## Evidence

变更前行为反例失败，变更后聚焦回归通过；集成门禁与交叉审查结果记录在 PR。仅离线请求/响应测试，无真实模型付费调用，未部署。

## Status

- InTest
