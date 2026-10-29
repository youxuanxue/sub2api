---
title: 用户历史用量账单查询与下载
status: approved
approved_by: "用户（本会话：同意。请研发实现。）"
approved_at: "2026-10-29"
created: "2026-10-29"
authors: [codex]
---

# 历史用量账单

## 授权与范围

用户确认在现有使用记录页增加历史账单：自注册以来/自定义日期、平台筛选、
日/月汇总、CSV/Excel 下载、明确时区与更新时间；本人查询与管理员指定用户共用口径。
该授权承接会话中的方案，不包含生产部署、迁移或修改扣费。生产旧明细保留期以前的
每日平台汇总仍在，现有逐请求查询与模型导出无法满足全历史验收。

用户在本会话下载验收后明确要求从文件移除汇总时间、查询时间、开始/结束日期、
汇总粒度、所选平台、最早/最新保留日期和周期状态；此要求作为导出字段调整的审批依据。

## 契约

- `GET /api/v1/usage/billing-history` 使用登录主体的 user ID；拒绝客户端 user_id。
- `GET /api/v1/admin/usage/billing-history` 位于现有管理员鉴权组，要求正整数 user_id。
- 查询：不传日期表示注册日到当前账单日；自定义 start_date/end_date 必须成对，结束日包含；
  granularity 为 day/month；省略 platform 表示全部，显式空字符串表示未标注平台。
  请求明细的 model、key、group、start_ts 等筛选不适用，误传返回 400。
- 日期使用日汇总状态中的 timezone_name，浏览器自动附带 timezone 不改变账单日。
  未初始化时区拒绝查询，不猜测日界。未来结束日、倒序或非法日期拒绝。
- 只读 `usage_dashboard_user_platform_daily`；单 SQL snapshot 同时读取用户注册时间、
  汇总时区和全部保留行。无 raw fallback，无生产数据回写、补数或 schema 迁移。
- 包含所有已计量请求（含零金额行），四类 token 和 actual_cost；不把 successful_* 当成功率。
  每日平台行是最细粒度，月度仅汇总选中日期，不扩为整月。无记录日不凭空补零。
- 返回用户、选择范围、注册时间、账单时区、USD、全用户已保留数据日期范围、可选平台、
  查询时间、各行/合计。行包含日期/月、平台、请求数、四类/总 token、实际费用、
  汇总时间、当日/当月未结束标记。覆盖日期只表示有记录的边界，不证明中间无缺漏。
- 当天沿用已有 feeder 快照，明确未结束且可能滞后；行更新时间与查询时间分开。
  月度更新时间为组成行最新计算时间，不代表所有历史日都重新计算过。
- Decimal 字符串保留费用精度；服务端 Decimal 累计。CSV 和 Excel 均导出当前成功查询快照，
  使用同一行/合计。文件仅包含用户、日期/月、平台、请求数、四类/总 token、实际费用和账单时区，
  不导出汇总时间、查询时间、开始/结束日期、汇总粒度、所选平台、最早/最新保留日期、周期状态；
  这些辅助信息仍在页面展示。Excel 金额为精确文本、请求及 token 为数值。
- 失败、换用户、换筛选、关闭或销毁组件立即失效旧数据，禁止下载旧用户/旧筛选的账单。
  取消/迟到响应不能覆盖新查询；生成 Excel 期间切换用户或关闭后不下载旧快照。
  页面缓存离开时同样取消并清空，返回时按保留的筛选重新查询。
- 查询超时上限 10 秒；最多支持 50,000 个用户日平台行，超限报错，不静默截断。
  接口返回 private/no-store。下载不发起第二条不同口径的取数请求。

## Owners

此表是本能力唯一 owner 清单。两个页面仅编排，同一共享组件和 composable 管理行为。

| 职责 | Owner |
|---|---|
| 公共数据类型 | `backend/internal/pkg/usagestats/historical_billing.go` |
| 一致只读快照 | `backend/internal/repository/usage_log_repo_historical_billing.go` |
| 日期/平台/月聚合与费用 | `backend/internal/service/usage_historical_billing.go` |
| 共用 HTTP 参数边界 | `backend/internal/handler/usagebilling/history.go` |
| 用户/管理员鉴权适配 | `backend/internal/handler/usage_historical_billing.go`、`backend/internal/handler/admin/usage_historical_billing.go` |
| 前端 API | `frontend/src/api/historicalBilling.ts` |
| 选择、取消、失败和切换状态 | `frontend/src/composables/useHistoricalBilling.ts` |
| 共享展示与下载入口 | `frontend/src/components/usage/HistoricalUsageBilling.vue` |
| CSV/Excel 字段与精度 | `frontend/src/utils/historicalBillingExport.ts` |

## 验收

[US-073](../../.testing/user-stories/stories/US-073-historical-usage-billing.md) 覆盖历史日记录不依赖
原始明细、月度日期裁剪、正金额精度、零费用、未标注平台、租户隔离、失效与下载文件。
上线不属于本次实现验收；生产用户端开放以独立发布验收为准。
