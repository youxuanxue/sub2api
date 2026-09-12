# US-054 Public Quickstart 与统一注册承诺

- ID: US-054
- Title: Public Quickstart 与统一注册承诺
- Priority: P1
- As a / I want / So that: 作为访客，我希望先查看客户端配置，在需要凭据时注册或登录，并回到原选择继续接入。
- Trace: `docs/approved/public-quickstart-registration-offer.md`
- Risk Focus:
  - 逻辑错误：注册关闭、邀请制、配置缺失与赠额必须诚实展示。
  - 行为回归：登录和邮箱注册都返回原客户端、协议或传输方式；已有 Key 使用原指南。
  - 安全问题：访客无 Key 查询、创建、导入或测试；返回路径不能外跳或携带凭据。
  - 运行时：设置刷新失败撤回历史承诺；退出登录后丢弃迟到的 Key 响应。

## Acceptance Criteria

1. AC-001（正向）：访客在现有 `/quickstart` 选择客户端、浏览占位配置；注册或登录后返回原选择，无自动创建或付费测试。
2. AC-002（负向）：closed / unavailable 不显示注册赠额；invitation_required 明示邀请条件；配置失效后所有入口同步撤回承诺。
3. AC-003（回归）：所有客户端继续使用共享配置生成器；已登录无 Key 由用户明确创建；受保护页面仍要求登录。
4. AC-004（安全/时序）：匿名请求不访问 Key 或能力接口；退出登录不恢复旧凭据；提交前按新设置重新验证。

## Assertions

断言 CTA 文案及目的地址、原客户端/协议选择、配置占位符、网络调用次数、Key 创建副作用、
注册配置的刷新失效与恢复、后端 API/HTML 注入一致且不含验证码密钥。

## Linked Tests

- `backend/internal/service/registration_offer_tk_test.go`::`TestRegistrationOfferPublicPolicyAndInjection`
- `backend/internal/service/registration_offer_tk_test.go`::`TestRegistrationOfferReadFailureAndRequiredCaptcha`
- Frontend: `frontend/src/components/auth/__tests__/RegistrationActionTk.spec.ts`, `frontend/src/components/keys/__tests__/UseKeyModal.spec.ts`, `frontend/src/views/user/__tests__/QuickstartView.spec.ts`, `frontend/src/utils/__tests__/quickstartJourney.tk.spec.ts`.
- Browser: `frontend/e2e/public-quickstart.e2e.ts` drives the real Vue UI with deterministic API fixtures.
- Run command: `cd backend && go test -tags=unit ./internal/service -run TestRegistrationOffer -count=1`
- Run command: `make test-frontend`
- Run command: `cd frontend && pnpm exec playwright test --config playwright.quickstart.config.ts`

## Evidence

本地 `make test`、`make test-frontend`、生产构建和 `scripts/preflight.sh` 通过。
Playwright 通过匿名配置、注册/邮箱验证/登录回跳、明确创建 Key、点击测试请求、
移动端、关闭/失效/邀请状态与提交前邮箱验证要求变更场景。
浏览器夹具验证 UI 旅程；不代表真实邮件投递、线上注册供给或上游计费调用。

## Status

- Done
