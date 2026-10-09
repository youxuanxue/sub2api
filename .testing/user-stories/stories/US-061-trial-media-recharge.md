# US-061 充值资格与 Studio 媒体错误提示

- ID: US-061
- Priority: P1
- Title: 充值资格与 Studio 媒体错误提示
- As a / I want / So that: 作为客户，我希望管理员充值后能正常使用媒体，并在试用被拦截时得到明确充值入口，避免字段口径不同造成错误拒绝。
- Trace: `docs/approved/trial-media-recharge-ux-and-total-recharged.md` §2 A1/B1、§3、§4。
- Risk Focus:
  - 逻辑错误：写入、面板、回填的 notes 精确匹配与正差额口径一致。
  - 行为回归：开户响应反映已提交累计充值；扣款不回退历史累计。
  - 安全问题：自动赠额不放行试用媒体；其他错误不提示错误的充值解决方法。
  - 运行时：journal 或累计字段更新失败时整个事务回滚；回填可重复执行且不修改余额或已删除用户。

## Acceptance Criteria

1. AC-001（正向）：开户或管理员正加款后，响应和数据库总充值一致；低余额用户仍获得媒体资格。
2. AC-002（负向）：三类赠额、扣款、零差额不增加累计充值；赠额明细仍可查，仅赠额用户媒体仍被拦截。
3. AC-003（回归）：回填按 A1/B1 重算，重复运行结果相同；已删除用户与余额不变。
4. AC-004（负向）：journal 或累计字段写入失败，余额、journal 和累计充值均不部分提交。
5. AC-005（UI）：Image / Video / BakeOff 接到媒体拒绝错误码后显示中文与去充值，点击到 `/purchase`；后续权限错误不残留充值 CTA。

## Assertions

比较持久化余额、累计充值、面板合计和媒体门禁结果；失败时断言数据均未提交。
浏览器检查可见中文、充值链接实际导航，以及后续错误不残留 CTA。

## Linked Tests

- AC-001: `backend/internal/repository/admin_balance_ledger_atomicity_integration_test.go`::`TestAdminService_CreateUser_ReturnsRechargedOpeningBalance`
- AC-001/002: `backend/internal/repository/admin_balance_ledger_atomicity_integration_test.go`::`TestAdminService_BalanceLedger_QualifyingTotals`
- AC-002: `backend/internal/repository/redeem_code_repo_integration_test.go`::`TestRedeemCodeRepoSuite`，子用例 `TestSumPositiveBalanceByUser_NoQualifyingCredits`。
- AC-003: `backend/internal/repository/admin_balance_ledger_atomicity_integration_test.go`::`TestTotalRechargedBackfill_QualifyingAndIdempotent`
- AC-004: `backend/internal/repository/admin_balance_ledger_atomicity_integration_test.go`::`TestAdminService_UpdateUserBalance_RollsBackOnLedgerFailure`
- AC-004: `backend/internal/repository/admin_balance_ledger_atomicity_integration_test.go`::`TestAdminService_BalanceLedger_RollsBackOnTotalRechargedFailure`
- Run command: `cd backend && go test -tags=integration ./internal/repository -run 'TestRedeemCodeRepoSuite/TestSumPositive|TestAdminService_.*(Ledger|OpeningBalance)|TestTotalRechargedBackfill' -count=1`
- AC-005: `frontend/e2e/studio-trial-recharge.e2e.ts` 使用 Playwright 操作真实 Vue UI，检查本地化、链接目标与后续错误的 CTA 移除。
- Run command: `cd frontend && pnpm exec playwright test --config playwright.studio-surface.config.ts studio-trial-recharge.e2e.ts`

## Evidence

本地 PostgreSQL 集成测试和 Chromium UI 验证通过；截图保存在 `frontend/e2e/artifacts/`。
浏览器 API 使用夹具，不声明真实支付或上游生成通过；未部署、未执行生产回填。

## Status

- Done
