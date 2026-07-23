# US-042-edge-handoff-one-time-code

- ID: US-042
- Title: Edge 管理 handoff 使用独立凭据与 child-owned PKCE 单次码
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **TokenKey 管理员**，我希望 **从 prod 控制台安全进入指定 Edge 管理页且不让会话凭据进入 URL 或 prod 页面**，**以便** 保留一键管理体验，同时把普通 mirror key、浏览器历史和跨域页面移出 session mint 暴露面。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §5。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G2。
  - 历史处置：US-041；本 Story 只交付替代协议与 legacy shutdown。
- Risk Focus:
  - 逻辑错误：prod 生成/持有 verifier，或错误 verifier/来源先消费合法 code。
  - 行为回归：popup 阻止、Edge 不可达或 capability 不足时恢复旧 token URL。
  - 安全问题：复用 mirror API key、宽松 `postMessage("*")`、code/token 进入 URL/日志/APM/console/artifact。
  - 运行时：多实例 Redis 竞态允许双 exchange，或 fleet 版本不齐时提前切换。

## Acceptance Criteria

1. **AC-001（独立最小权限凭据）**：Given Edge handoff control client，When 调用 mirror read、relay、account write 或 exchange，Then 全部拒绝；只有签名正确、timestamp/nonce/key ID 合法的 mint 被接受。
2. **AC-002（child-owned PKCE）**：Given prod 同步打开 clean Edge child，When child READY，Then verifier 只存在 child memory，prod 仅收到 S256 challenge 与 child nonce。
3. **AC-003（绑定 mint）**：Given 合法 prod admin 请求，When prod backend 调 Edge mint，Then code 绑定 Edge audience、允许的 prod source origin、window/child nonce、challenge、admin subject、attempt 与不超过 60 秒 TTL。
4. **AC-004（原子 exchange）**：Given 一个 code，When同源 exchange 提交 code、verifier、source origin 与 window nonce，Then Edge 原子校验全部绑定；错 verifier/错来源/错 audience/过期/并发重放均 fail closed，错误尝试不删除合法记录，成功并发只有一个 winner。
5. **AC-005（会话隔离）**：Given exchange 成功，When Edge 返回 access/refresh pair，Then 仅 Edge child 同源响应可见，refresh family 标记为 `edge_handoff`，prod backend/SPA 不接收 session。
6. **AC-006（浏览器工件零秘密）**：Given 完整真实双 origin 旅程，When 检查地址栏、history、Referer、console、analytics、APM 和允许保留的 trace/screenshot，Then code、verifier、access、refresh 和 mirror/control secret-pattern 全部为零。
7. **AC-007（失败 UX）**：Given popup blocked/closed、timeout、capability 不足或 Edge 离线，When handoff 失败，Then UI 提供手动 Edge 登录，不导航到或构造 legacy token URL。
8. **AC-008（fleet 与 legacy）**：Given 所有 deployable Edge capability 通过且新流量稳定，When 经人工批准 shutdown，Then旧 mint 返回 410、普通 mirror key 无 mint 权限、源码无 token URL builder；rollback 只回新流或手工登录。

## Assertions

- HMAC secret 与 mirror account API key 使用不同 secret owner、key ID 和轮换周期。
- `postMessage` 两端都验证 exact origin 和 exact window source。
- transient child 的 deployed opener policy 不得在握手前切断 `window.opener`，response-header E2E 必须覆盖。
- stable attempt ID 通过 Redis 当前-code-hash 指针实现替换；mint retry 生成新 code 并使旧未消费 hash 失效，不承诺重放同一明文 code。
- exchange 响应 `Cache-Control: no-store`，请求正文与响应正文禁止日志/APM capture。

## Linked Tests

- `backend/internal/handler/edge_handoff_authorization_test.go`::`TestEdgeHandoffMintRequiresDedicatedSignedClient` *(planned)*
- `backend/internal/service/edge_handoff_code_store_integration_test.go`::`TestEdgeHandoffExchangeIsBoundAndSingleUse` *(planned)*
- `backend/internal/service/edge_handoff_code_store_integration_test.go`::`TestEdgeHandoffWrongVerifierDoesNotConsumeCode` *(planned)*
- `frontend/e2e/edge-handoff.spec.ts`::`handoff keeps every credential out of URL and retained browser artifacts` *(planned)*
- `frontend/e2e/edge-handoff.spec.ts`::`blocked popup and old edge fall back to manual login` *(planned)*

运行命令：

```bash
cd backend && go test -tags=unit ./internal/handler ./internal/service -run EdgeHandoff -count=1
cd frontend && npm run test:e2e -- edge-handoff.spec.ts
```

## Evidence

- 实现 PR 附 Redis 并发测试、双 origin Playwright trace 的 secret scan、fleet capability report 与 legacy 零调用窗口。

## Status

- Ready — 等待设计批准；不得保留或恢复现有 token-fragment handoff。
