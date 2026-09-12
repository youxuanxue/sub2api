# US-056 Edge 管理会话安全交接

- ID: US-056
- Priority: P1
- Title: 一键进入 Edge，读取凭据不能签发管理会话
- As a / I want / So that: 管理员希望从控制台一键进入 Edge，同时凭据只在对应安全边界内流转。
- Trace: `docs/approved/edge-admin-handoff-v2.md`（pending，仅方向已获同意）
- Risk Focus:
  - 逻辑错误：授权过期、重复使用、错误 proof 或 attempt 不应建立会话。
  - 行为回归：两个管理入口继续一键进入；不可用时仍能直接登录。
  - 安全问题：独立签发权限、固定 audience、精确窗口绑定、凭据不进 URL/父页面。
  - 运行时：并发兑换、Redis 故障、迟到响应、权限撤销、会话签发失败。

## Acceptance Criteria

1. AC-001（正向）：管理员点击后打开干净的 Edge 页面，兑换会话并进入账号管理。
2. AC-002（负向）：镜像 Key 不能签发会话；篡改、错误目标、过期与重放均被拒绝。
3. AC-003（安全）：错误 verifier 或窗口来源不能完成交接，也不能消费合法兑换记录。
4. AC-004（并发）：同一交接码只有一次成功兑换；Redis 故障不得绕过消费检查。
5. AC-005（回归）：控制台两个入口共享同一生命周期 owner；失败提供重试和直接登录。
6. AC-006（撤销）：Edge 管理员被停用后无法兑换；签发后的会话族可按交接审计标识撤销。

## Assertions

断言返回凭据的接收窗口、URL、错误返回、消费次数、重放和伪造窗口消息的实际拒绝行为。

## Linked Tests

- `.testing/prototypes/edge-handoff/protocol.test.mjs`（协议原型）
- `.testing/prototypes/edge-handoff/browser.test.mjs`（真实浏览器原型）
- Run command: `node --test .testing/prototypes/edge-handoff/protocol.test.mjs`
- Run command: `pnpm --dir frontend exec playwright test --config ../.testing/prototypes/edge-handoff/playwright.config.mjs`

## Evidence

本地协议测试及浏览器测试已运行通过。覆盖原型的签名、一次性消费、期限、proof、
不同源窗口与失败回退。CI frontend job 调用同样命令。

生产 AC 尚未验收：原型使用模拟管理员、内存 Map 和进程内转发，不能证明真实 JWT /
Redis 原子性、生产权限变更、会话族撤销或线上凭据迁移。见设计的 Evidence limits。

## Status

- Draft
