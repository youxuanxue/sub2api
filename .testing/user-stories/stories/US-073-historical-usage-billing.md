# US-073-historical-usage-billing

- ID: US-073
- Priority: P1
- Title: 用户自助查询与下载完整保留历史的每日平台用量账单
- As a / I want / So that: 作为客户或代查管理员，我要按日/月查询及下载自注册以来的用量和实际费用，以便验收与对账。
- Trace: [审批基线](../../../docs/approved/historical-usage-billing.md)；角色×查询/筛选/下载能力；原始明细过期但日汇总仍保留。
- Risk Focus:
  - 逻辑错误：月度仅聚合所选日、四类 token、零金额、十进制费用与时区日界。
  - 行为回归：现有使用记录/导出入口保持，历史账单独立且两端共用 owner。
  - 安全问题：用户只能读本人、管理员显式指定用户、CSV 公式中和、迟到响应不得泄漏旧用户数据。
  - 运行时问题：超时失败不显示旧金额；关闭/换筛选/卸载取消请求；Excel 生成期间切换禁止旧下载。

## Acceptance Criteria

1. AC-001 (正向): Given 六月日平台汇总且原始请求不存在，When 查询自注册以来或按月筛选，Then 保留六月真实请求数、四类 token、实际费用，当前日标为未结束。
2. AC-002 (负向): Given 普通登录用户，When 提交其他 user_id、请求级筛选或非法日期，Then 拒绝越权/不支持条件且不返回另一用户金额；无认证拒绝。
3. AC-003 (正向): Given 已查询账单，When 下载 CSV/Excel，Then 文件与当前快照的日期/平台/汇总/精确金额一致，无第二次查询，不执行文本公式。
4. AC-004 (负向): Given 已显示账单，When 换用户后旧请求迟到或刷新失败，Then 不显示/下载旧账单；重试仅恢复当前筛选结果。
5. AC-005 (回归): Given 用户端和管理端使用记录页，When 经真实 UI 打开账单、按月/平台筛选和下载，Then 两端同口径，原有记录页仍可加载，窄屏账单卡片不溢出（宽表在卡片内滚动）。

## Assertions

- 金额 `0.1000000001 + 0.2000000002 = 0.3000000003` 精确，零消费行仍计请求/token。
- 未标注平台空字符串与全部平台不同；月度不把自定义窗口扩为整月。
- 无 raw logs 的 PostgreSQL fixture 返回原历史账单，其他用户的 999 USD 不进入合计。
- HTTP 普通用户不可传 user_id；管理员必传正整数；两端返回同一账单。
- 下载的 Excel 文本金额与 CSV 精度保持，平台文本不执行公式；失败后表格和下载入口不再展示旧数据。

## Linked Tests

- `backend/internal/service/usage_historical_billing_test.go`::`TestUS073_HistoricalBillingRetainsDailyAndMonthlyCharges` — AC-001。
- `backend/internal/service/usage_historical_billing_test.go`::`TestUS073_HistoricalBillingValidationAndFailures` — AC-002。
- `backend/internal/service/usage_historical_billing_test.go`::`TestUS073_HistoricalBillingTimezoneEmptyAndInvalidMoney` — AC-001/002。
- `backend/internal/handler/usage_historical_billing_test.go`::`TestUS073_HistoricalBillingUserIsolationAndAdminParity` — AC-002/005。
- PostgreSQL integration: `backend/internal/repository/usage_log_repo_historical_billing_integration_test.go`，suite 方法 `TestUS073_HistorySurvivesRawRetentionAndIsTenantScoped` — AC-001/002（命令见下）。
- `frontend/src/composables/__tests__/useHistoricalBilling.spec.ts` — AC-004。
- `frontend/src/utils/__tests__/historicalBillingExport.spec.ts` — AC-003。
- `frontend/e2e/historical-billing.e2e.ts` — AC-003/004/005，Playwright 真实 UI + 本地 API fixture，下载文件解码验证。

运行命令：

```bash
cd backend && go test -tags=unit ./internal/service ./internal/repository ./internal/handler -run TestUS073_ -count=1
cd backend && go test -tags=integration ./internal/repository -run 'TestUsageLogRepoSuite/TestUS073_' -count=1 -v
pnpm --dir frontend exec vitest run src/composables/__tests__/useHistoricalBilling.spec.ts src/utils/__tests__/historicalBillingExport.spec.ts
pnpm --dir frontend exec playwright test --config playwright.historical-billing.config.ts
```

## Evidence

已通过 Go 聚焦单元测试、真实 PostgreSQL 集成测试、前端状态/文件单元测试与用户/管理员 Playwright UI 下载测试。浏览器证据保存在 `frontend/.cache/historical-billing-results/`。
本地 PostgreSQL 与本地 UI fixture 验证不代表已上线。

## Status

- [x] Done
