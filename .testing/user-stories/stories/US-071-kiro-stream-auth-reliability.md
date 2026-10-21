# US-071-kiro-stream-auth-reliability

- ID: US-071
- Title: Kiro 协议、项目规则与凭据可靠性
- Priority: P1
- As a / I want / So that: 作为 Kiro 用户，我希望损坏响应明确失败、项目规则和合法工具调用完整保留、账号刷新安全持续，以避免重复任务、乱码和账号掉线。
- Trace: [审批基线](../../../docs/approved/kiro-stream-auth-reliability.md)；#2764；开源缺陷核实。
- Risk Focus:
  - 逻辑错误：帧校验、迟到 ID、刷新响应与凭据合并。
  - 行为回归：合法重复文本、终止后 metering、流式/非流式工具结构、客户 system。
  - 安全问题：region 改写主机、重定向外发凭据、错误泄露 token。
  - 运行时问题：帧分配上限、取消传播、禁止损坏协议重复发送、profile 旁路轮换。

## Acceptance Criteria

1. AC-001 (正向): Given 合法增量/工具/用量 When Forward 转换 Then 文本和参数保持一致，唯一工具 ID 与终止有效。
2. AC-002 (负向): Given CRC/JSON/UTF-8/长度异常或终止后内容 When 解析 Then 返回错误，不输出成功结束，不跨 endpoint 重放损坏协议。
3. AC-003 (正向/负向): Given 工具 ID 迟到 When 唯一序列完成 Then 仅交付一次真实 ID；歧义、缺 ID、重复完成或损坏参数拒绝。
4. AC-004 (回归): Given Claude Code system 与追加规则 When 转换 Then 仅替换精确身份行，保留全部项目规则和工具约束。
5. AC-005 (正向/负向): Given OAuth 刷新 When 上游省略 refresh Then 保留旧值；合法轮换保留新值；无效 access/expiry 或取消拒绝保存。
6. AC-006 (负向): Given 非法 region、重定向或 profile 查询失败 When 请求 Then 不向异常 host 发送凭据、不在 profile 查询里私自轮换、不在错误中泄露 token。

## Assertions

- AC-001/002 → TestKiroReliabilityGatewayFaultImpact、TestKiroReliabilityFrameValidation：精确断言失败、调用次数、无 message_stop/替换字符；终止后用量仍可累计。
- AC-003 → TestKiroReliabilityLateToolID、TestKiroReliabilityGatewayLateIdentity：实际 ID、工具名、参数对象和终止原因。
- AC-004 → TestKiroReliabilityPreservesProjectRules：字符串与 block 形式的项目规则不丢失。
- AC-005 → TestKiroReliabilityRefreshValidation、TestKiroReliabilityRefreshService、TestKiroReliabilityRefreshCancellation：返回值、合并结果、真实请求上下文取消。
- AC-006 → TestKiroReliabilityRefreshRegionAndRedirect、TestKiroReliabilityProfileDoesNotRotateCredentials、TestKiroReliabilityRefreshErrorsDoNotLeakCredentials：真实 HTTP redirect 行为、目的主机、调用次数、凭据未变。

## Linked Tests

- `backend/internal/integration/kiro/reliability_test.go`::`TestKiroReliabilityFrameValidation`
- `backend/internal/integration/kiro/reliability_test.go`::`TestKiroReliabilityLateToolID`
- `backend/internal/integration/kiro/reliability_test.go`::`TestKiroReliabilityUnicodeEscapes`
- `backend/internal/integration/kiro/prompt_filter_test.go`::`TestKiroReliabilityPreservesProjectRules`
- `backend/internal/integration/kiro/refresh_test.go`::`TestKiroReliabilityRefreshValidation`
- `backend/internal/integration/kiro/refresh_test.go`::`TestKiroReliabilityRefreshRegionAndRedirect`
- `backend/internal/integration/kiro/refresh_test.go`::`TestKiroReliabilityRefreshCancellation`
- `backend/internal/integration/kiro/refresh_test.go`::`TestKiroReliabilityProfileDoesNotRotateCredentials`
- `backend/internal/integration/kiro/refresh_test.go`::`TestKiroReliabilityRefreshErrorsDoNotLeakCredentials`
- `backend/internal/service/kiro_reliability_test.go`::`TestKiroReliabilityGatewayFaultImpact`
- `backend/internal/service/kiro_reliability_test.go`::`TestKiroReliabilityGatewayLateIdentity`
- `backend/internal/service/kiro_refresh_reliability_test.go`::`TestKiroReliabilityRefreshService`
- 运行命令：`cd backend && go test -p 4 -tags=unit ./internal/integration/kiro ./internal/service ./internal/handler -run 'Kiro|ClaudeToKiro|ParseEventStream|ApplyPromptFilters|OAuthRefreshAPI' -count=1`

## Evidence

- 修复前反例：`/tmp/tk-kiro-reliability-red.log`。
- 修复回归：`/tmp/tk-kiro-reliability-regression.log`；真实调用证据与局限记录在核实报告。
- 无 UI 变更，API/协议测试不冒充浏览器 e2e。

## Status

- InTest
