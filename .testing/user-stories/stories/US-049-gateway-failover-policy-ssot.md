# US-049-gateway-failover-policy-ssot

- ID: US-049
- Title: Gateway failover 判定使用全局单一 owner
- Priority: P0
- As a / I want / So that: 作为网关调用方，我希望相同的上游失败语义在所有平台和 channel adapter 中由同一策略判定，从而避免新增路径遗漏换号或错误耗尽账号池。
- Trace: `docs/approved/gateway-failover-policy-ssot.md`
- Risk Focus:
  - 逻辑错误：profile 状态矩阵、semantic 覆盖顺序和 passthrough 账号配置必须保持既有差异。
  - 行为回归：Generic、OpenAI HTTP/SSE、Google、Grok、NewAPI bridge、passthrough 和 Live 的既有结果不变。
  - 安全问题：未知 profile/semantic fail closed，确定性 client/policy failure 不得轮换并消耗共享账号池。
  - 运行时问题：本次只集中纯判定，不改变执行循环、同账号重试、退避、账号处罚或已输出流重放，避免扩大请求时延与副作用。

## Acceptance Criteria

1. AC-001 (正向): Given 任一已支持 profile 的账号级或瞬时故障事实 When adapter 提交 observation Then 全局 policy 返回 `RetryNextAccount=true`。
2. AC-002 (负向): Given shared fault semantic 或未知 profile/semantic When 全局 policy 判定 Then fail closed，不切换账号。
3. AC-003 (回归): Given 各 profile 的既有 status matrix When 执行全局矩阵测试 Then Generic、OpenAI/Grok、Google、NewAPI bridge 和 passthrough 结果逐项保持。
4. AC-004 (回归): Given OpenAI HTTP/SSE、Grok body、Anthropic compatibility 400 和 Live transport error When 经原 adapter 判定 Then 结果继续符合原协议语义且最终调用全局 owner。
5. AC-005 (机械守卫): Given 上游 merge 删除 owner、测试或 adapter 接线，新增未委托全局 owner 的 `shouldFailover` / `retryNextAccount` facade，绕过 `ShouldRetryNextAccount` runtime choke point，恢复 decision bool/action，或用 verdict 型 semantic 伪装决策 When 运行 gateway sentinel Then preflight 失败。

## Assertions

- semantic 先于 status，shared 503 不 failover，account fault 400 会 failover。
- `response.failed` 未知结构默认 failover，通用 SSE `error` 未知结构默认 terminal。
- OAuth passthrough 普通 500 terminal；API key passthrough 500 failover；账号自定义 pool retry 可覆盖未列出的 status。
- NewAPI bridge 普通 500 terminal，而 502/503/504 failover；arrears semantic 可让 400/403 failover。
- Grok body classifier 不再输出 failover 布尔字段。
- 所有 handler 对 `UpstreamFailoverError` 的最终 retry/stop 判定都经过全局 owner；legacy 空 scope 仍 retry，未知 scope fail closed。

## Linked Tests

- `backend/internal/service/gateway_failover_policy_test.go`::`TestUS049_GlobalGatewayFailoverPolicyMatrix`
- `backend/internal/service/gateway_failover_policy_test.go`::`TestUS049_SemanticOverridesStatusAndFailsClosed`
- `backend/internal/service/gateway_failover_policy_test.go`::`TestUS049_RuntimeFailoverChokePointUsesGlobalPolicy`
- `backend/internal/service/gateway_failover_policy_test.go`::`TestUS049_OpenAIPassthroughAccountMatrix`
- `backend/internal/service/gateway_failover_policy_test.go`::`TestUS049_AdaptersDelegateWithoutBehaviorDrift`
- Run:

```bash
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -run 'TestUS049_' -count=1
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -count=1
cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/handler -count=1
python3 scripts/sentinels/check-gateway-tk.py
python3 .testing/user-stories/verify_quality.py
```

## Evidence

- `TestUS049_`: PASS.
- Full `internal/service` unit package: PASS (`ok github.com/Wei-Shaw/sub2api/internal/service 112.341s`).
- Full `internal/handler` unit package: PASS (`ok github.com/Wei-Shaw/sub2api/internal/handler 10.559s`).
- Gateway TK sentinel registry and global invariant: PASS (`855/855 intact`).
- Story quality: PASS (`45 stories, 0 issues`).

## Status

- [x] Done
