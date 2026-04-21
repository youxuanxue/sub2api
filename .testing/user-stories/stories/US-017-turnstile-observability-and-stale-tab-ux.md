# US-017-turnstile-observability-and-stale-tab-ux

- ID: US-017
- Title: Turnstile siteverify 失败可观测 + 全部登录态入口 stale-tab UX 自救引导
- Version: V1.0（Hotfix）
- Priority: P0
- As a / I want / So that:
  作为 **运维 / on-call 工程师**，我希望 Turnstile 失败时一条结构化日志就能定位根因
  （token 是否到达后端、CF 端口是否健康、CF 拒绝原因是什么），以便在用户报告
  "登录验证失败" 时能在 30 秒内判断是「stale tab」「CF 限流」还是「真实攻击」，
  而不是像 2026-04-20 那样要远程抓 token 反推。

  作为 **终端用户**，我希望在最常见的 stale-tab 场景下被明确告知「请刷新页面」，
  而不是看见无操作建议的「Verification failed」原地循环。

- Trace:
  - 系统事件：Cloudflare siteverify 失败（403/429/5xx/200+success=false）
  - 防御需求：日志泄漏 token → 一次性凭证被复用风险（详见 §AC-002）
  - 角色 × 能力：所有匿名用户 × 4 个登录态入口（login / register / forgot-password / email-verify）

- Risk Focus:
  - **逻辑错误**：repository 层 `*response` 在 JSON 解析失败时丢失 HTTPStatusCode → 上层日志退化为「无上下文 decode error」（已发生过，2026-04-20）
  - **安全问题**：日志记录 token 时不能完整暴露——CF token 是一次性凭证，泄漏即可被滥用一次。约束：prefix+suffix 必须隐藏中段 ≥4 字节
  - **行为回归**：`url.Values.Encode()` 改写 token 字节会被 CF 误判为 invalid-input-response（曾被怀疑过的根因，需永久钉死）
  - **运行时问题**：CF edge 偶发返回 502 HTML（非 JSON）必须仍带 status/latency 给上层做根因分类
  - **UX 漂移**：4 个入口 view 各自手写 catch 分支 → 出现一个改 4 处的漂移源（这次就发生了）；本 PR 收敛到 `buildAuthErrorMessage(reasonOverrides)` 单点

## Acceptance Criteria

1. **AC-001（正向 / 后端可观测）**：Given Turnstile 启用且 secret 已配置，When CF 返回 `success=false`，Then service 层输出一条 `warn` 级别的结构化日志，message=`[Turnstile] siteverify returned success=false`，**必须**含全部字段：`component / remote_ip / token_len / token_prefix / token_suffix / http_status / latency_ms / cf_hostname / cf_action / cf_cdata / cf_challenge_ts / cf_error_codes`。

2. **AC-002（负向 / 安全 — token 摘要不泄漏）**：Given 任意长度 ≥20 字节的 token，When 调 `summarizeToken(token)`，Then `len(prefix)+len(suffix) ≤ len(token)-4`（中段至少藏 4 字节）；且 `prefix+suffix ≠ token` 任何拼接序列。

3. **AC-003（负向 / 运行时 — repository 契约不退化）**：Given Cloudflare edge 返回非 JSON 的 502 HTML，When `repository.VerifyToken` 解析失败，Then 必须返回 `(*response, err)` 的二元组（`response != nil`），其中 `response.HTTPStatusCode == 502` + `response.LatencyMs >= 0`。`response == nil` 仅保留给「网络层失败（拨号/TLS）」一种场景。

4. **AC-004（负向 / 行为回归 — token 字节保真）**：Given 含 `+/=._-:&?` 等 URL 特殊字符的 token，When 经 `url.Values.Encode()` 编码后被 CF siteverify 接收，Then 服务端解析出来的 `response` 字段必须与原 token 逐字节一致。

5. **AC-005（正向 / 前端 UX）**：Given 4 个 auth 入口（login / register / forgot-password / email-verify）任一，When 后端返回 `reason === 'TURNSTILE_VERIFICATION_FAILED'`，Then 用户看到的 `errorMessage` 是 `auth.turnstileFailedRefresh` 文案（"请刷新页面后重试"），**而不是** detail/message 原文或通用 fallback。

6. **AC-006（负向 / UX — reasonOverrides 不越权）**：Given 后端返回 `reason === 'INVALID_CREDENTIALS'`，When `buildAuthErrorMessage` 的 `reasonOverrides` 只覆盖 `TURNSTILE_VERIFICATION_FAILED`，Then 必须落回 `response.data.detail`（即用户看到「密码错误」而不是「请刷新页面」）。

7. **AC-007（回归保护）**：Given 代码变更，When 执行 `TestUS017_*` 全部测试 + `authError.spec.ts`，Then 全部通过。

## Assertions

- 后端结构化日志：使用 `zaptest`/captureSink 抓取，断言 `failureEvent.Fields[k]` 对每个必填字段存在且类型正确（特别地 `cf_error_codes` 在 `MapObjectEncoder` 下编码为 `[]interface{}`，已 probe 验证；类型变化必须 fail 而非 fallback）
- 安全约束：`require.LessOrEqual(t, len(pre)+len(suf), tc.wantLen-4)` 钉死隐藏字节数
- repository 契约：`require.NotNil(t, resp)` + `require.Equal(t, http.StatusBadGateway, resp.HTTPStatusCode)` 同时满足
- byte-preservation：服务端 handler 内部 `require.Equal(t, trickyToken, values.Get("response"))`
- 前端 reasonOverrides：`expect(message).toBe('Stale verification token — refresh and try again')` 且非命中场景 `expect(message).toBe('wrong password')`

## Linked Tests

- `backend/internal/service/turnstile_observability_test.go`::`TestSummarizeToken_NeverLeaksFullToken`
- `backend/internal/service/turnstile_observability_test.go`::`TestVerifyToken_FailureLogContainsAllDiagnosticFields`
- `backend/internal/service/turnstile_observability_test.go`::`TestVerifyToken_EmptyTokenLogsExplicitly`
- `backend/internal/repository/turnstile_service_test.go`::`TestTurnstileServiceSuite/TestVerifyToken_PopulatesHTTPStatusAndLatency`
- `backend/internal/repository/turnstile_service_test.go`::`TestTurnstileServiceSuite/TestVerifyToken_NonOKStatusStillReturnsResponse`
- `backend/internal/repository/turnstile_service_test.go`::`TestTurnstileServiceSuite/TestVerifyToken_NonJSONResponseStillCarriesStatus`
- `backend/internal/repository/turnstile_service_test.go`::`TestTurnstileServiceSuite/TestVerifyToken_BytePreservationOfTrickyChars`
- `frontend/src/utils/__tests__/authError.spec.ts`::`reasonOverrides wins over response.data.detail when reason matches`
- `frontend/src/utils/__tests__/authError.spec.ts`::`reasonOverrides only applies when reason is in the override map`
- `frontend/src/utils/__tests__/authError.spec.ts`::`reasonOverrides reads reason from response.data.reason when top-level missing`

运行命令：

```bash
# 后端（unit + 默认）
cd backend && go test -tags=unit -v -run 'TestSummarizeToken_NeverLeaksFullToken|TestVerifyToken_FailureLogContainsAllDiagnosticFields|TestVerifyToken_EmptyTokenLogsExplicitly' ./internal/service/...
cd backend && go test -v -run 'TestTurnstileServiceSuite' ./internal/repository/...

# 前端
cd frontend && pnpm vitest run src/utils/__tests__/authError.spec.ts
```

## Evidence

- PR：https://github.com/youxuanxue/sub2api/pull/20
- 故障复盘根因：2026-04-20 prod-hotfix 调试链路（远程 SSM + CF dashboard 排查后确认为 stale browser tab → 自愈，但耗时数小时）

## Status

- [x] InTest（待 CI 全绿后翻 Done）
