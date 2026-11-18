---
title: TokenKey upstream merge 2026-11-18 isolation and migration scope
status: draft
approved_by: pending
authors: [codex]
created: 2026-11-18
---

# 2026-11-18 上游隔离合并范围

本次用户调用 `$tokenkey-upstream-merge`，授权按技能完成隔离分支上的上游合并、冲突裁决与验证。本文记录待最终审核的迁移与行为边界；主线合并、生产部署、线上迁移、模型激活和价格发布另行确认。

固定 fork 基线为 `d581b62cb6fa68c12e1cd4c8521c02b76492f5ba`，上游目标为 `9e9ca6fe95d3d20ca9d099b0c681e336e8d812c4`，共同祖先为 `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484`。实际合并与最终审计使用这组 SHA，保持上游 ancestry。

## 数据库增量

- `251_group_force_openai_ultrafast.sql`：分组增加 `force_openai_ultrafast`，默认 false，沿用已存在的 Fast/Ultrafast 设置与请求策略入口。
- `251_group_usage_rollup_nonblocking_build.sql`：汇总状态增加过期下界、重建目标与脏日期，调整失效函数和触发器，使重建计算不长期阻塞 usage 写入。验证必须覆盖并发写入、更新、删除、过期和重建发布水位。
- `252_channel_pricing_ultrafast_multiplier.sql`：渠道增加可空 Ultra Fast 倍率及正值约束；NULL 沿用模型默认倍率。
- 迁移执行器以完整文件名排序和记账，同号 `251_*` 保持各自原名，既有迁移 checksum 不变。Ent 从 schema 重新生成，Wire 从声明重新生成。
- 集成测试只运行隔离 PostgreSQL/Redis；生产锁等待、DDL 和触发器变更需在发布前审核。回滚应用不逆向删除新增列。

## 行为保留与冲突裁决

- Candidate eligibility、实际账号与 `protocolrouter.Plan` 保持既有 owner；计费组平台不参与执行选号与 handler 裁决。
- TokenKey pricing registry、scope 优先级、客户扣费与 response cost preview 保持既有契约。上游模型元数据和 fallback 价格不构成 TokenKey 价格发布、目录承诺或账号激活。
- 上游新增 Ultra Fast 配置保留，默认关闭；显式启用时的模型门控、账号资格、请求最终 tier 与结算一致性一起验证，不能只接字段或 UI。
- 保留 TokenKey 持久化 hold 与异步结算生命周期，审阅上游 billing lane 的有界队列、失败与关闭路径，避免双重预留、提前释放或静默丢结算。
- 上游 gateway 修复与主线已回补的 P0 行为合并到共享 owner，保留首输出失败切换、工具参数、thinking、错误与 terminal 证据。QA raw capture 先脱敏再持久化，capture fail-open 不等于 silent-loss。
- 保留 UI 共享 composable/component、admin/user persistent shell、动态模型目录与接入指南；新增词条取并集，测试断言迁移必须有对应的新验证。
- 已删除的 fork 旧架构保持删除，逐项迁移上游修复意图；不将未运行、删除或过滤的测试写成通过。

## 验证边界

先记录固定 fork 基线的既有验证，再在合并后运行后端 unit/integration、Ent/Wire 生成、前端 lint/typecheck/unit/build、真实 Playwright UI、agent contract 和完整 preflight。最终审计核对 fork 修改文件、测试断言与 CI 过滤的变化，并生成 upstream rollout summary。

实际运行结果和未覆盖项由本轮审计记录与 PR 承载；本文本身不宣称已验证、已合入或已部署。


## 上游改变 TokenKey 行为的审阅记录

| 受影响行为 | 裁决及理由 | 对应验证 |
| --- | --- | --- |
| 远端 pricing/fallback/override 覆盖全局价 | 保留解析与下载为 sensor；运行期仍只发布 registry。拒绝用上游默认价重写 GLM 人民币税基、Gemini 稳定价、Codex/Grok alias owner，或自动上架未知模型 | pricing registry publisher、sensor 字段解析、DeepSeek override/reload、显式 scope 计费测试 |
| 上游 billing lane 包装仓储 | 保留普通账单批量优化；装饰器转发余额 hold、过期退款及视频退款。带 hold 的批次在同一事务按原 owner 顺序结算，避免 hold 留存或重复扣费 | `TestBillingLaneAndBatchPreserveHandedOffHolds`、lane 能力转发与失败测试 |
| usage rollup 长事务与逐批显式失效 | 接受非阻塞 rebuild / publish 与触发器失效，保留过期下界和发布水位一致性 | 下表的原断言迁移，以及真实 PostgreSQL 并发/删除/恢复集成测试 |
| 依据月份名称 DROP 分区 | 拒绝：时区边界可使“六月分区”仍包含应保留的七月记录。使用共享 catalog-bound owner，锁超时跳过 DROP 并受限删除过期行 | `CleanupUsageLogsPreservesPartitionBeyondActualCutoff`、DropExpiredWith 回调与 fail-closed 测试 |
| Messages keepalive 后仍可切换账号 | 保留 TokenKey 已发送 message_start 即提交的边界；无内容输出前仍可切换，已提交流以 SSE error 结束 | handler/service pre-content failover 测试 |
| 未启用 source filter 的 passthrough 默认忽略映射 | 仅保留直接 legacy 方法的兼容语义；真实调度继续守 model_mapping 白名单，显式 opt-in filter 可列出 source 名称，兄弟账号映射仍可见 | passthrough 资格、混合池、wire body 与 catalog 测试 |
| upstream admin 页面、用量和接入指南 | 保留 persistent shell、共享模型目录、被动用量接口与恢复快照；新增字段接到已有 owner | 前端全量测试、账户用量/接入指南/设置表单桌面与移动端 Playwright |

没有改动 CI workflow 或必测文件过滤；不删除 TokenKey 特性文件。删除的未调用包装函数由已有共享 owner 承接，Gemini 流式工具参数使用保留 partial JSON 的 `geminiToolCallArgsJSON`，对象形参数仍由 `normalizeGeminiFunctionArgs` 统一规范。

## 原有断言迁移

这些测试的旧 SQL/缓存调用顺序不再代表用户行为，保留其一致性断言并迁移到新 owner：

| 固定 fork 基线中的测试 | 新验证 |
| --- | --- |
| `SyncGroupUsageRollupsNoopsAtCurrentDate` | `SyncGroupUsageRollupsNoopsWithoutLockingAtCurrentDate` |
| `SyncGroupUsageRollupsPublishesWatermarkLast` | `SyncGroupUsageRollupsBuildsAndPublishesInSeparateTransactions`、`PublishKeepsUnmarkedInvalidation` |
| `RecomputeRangeRebuildsGroupRollupsBeforeCommit` | `RecomputeRangeLeavesGroupRollupRebuildToSync`、`RecomputeRangeInvalidatesGroupRollupsBeforeDashboardRebuild` |
| `CleanupUsageLogsNonPartitionedInvalidatesEachBatchAndSyncs` | `CleanupUsageLogsExpiresWholeDaysBeforeDeleting`、触发器 expired floor 集成测试 |
| `CleanupUsageLogsPartitionedSortsAndInvalidatesEachDropBeforeSync` | `CleanupUsageLogsPreservesPartitionBeyondActualCutoff`、锁超时与回滚测试 |
| `CleanupUsageLogsNonPartitionedFailureRollsBackWithoutSync` | `CleanupUsageLogsBatchFailureStopsWithoutSync` |
| `DeleteUsageLogsBatchAtomicallyInvalidatesGroupRollups`、`RollsBackWhenInvalidationFails` | `DeleteUsageLogsBatchDoesNotLockGroupRollupState`、`QueryError`；真实 PostgreSQL 的 `GroupUsageRollupTriggerInvalidatesCascadedHistoricalDelete`、`GroupUsageRollupBulkDeleteDuringBuildWritesStateOnce` 验证同事务触发器失效 |
| `SchedulerCacheSnapshotMemoSecondReadOnlyFetchesTagsAndLastUsed` | `SchedulerCacheSnapshotMemoRepeatReadsFetchTagsAndExpiredLastUsed`、`LastUsedWriteInvalidatesLocalCache`，验证短时复用、过期刷新与本进程写入立即可见 |

## 迁移规模证据

2026-11-18 对 prod/cn-prod 仅运行 read-only SELECT，均为 PostgreSQL 18.6；groups 分别为 114/12 行，channel_model_pricing 均为 0 行，rollup state 均为 1 行。新增迁移显式设置 3s lock_timeout 与 30s statement_timeout，并声明 expand-only 应用回滚保留列。采样命令中的 100ms/2s 超时是探针自身预算，不代表数据库或应用默认值。未执行生产 DDL。

补充审阅：保留 TokenKey Gemini direct converter 的 tools + response schema 同传契约，上游 legacy helper 的跳过策略不覆盖该 owner；真实 forward 回归验证 JSON 格式仍在 wire 中。修复 compat stream 已输出后仅处理 credit exhaustion 的遗漏：所有 terminal error 统一交既有账号健康 owner，保持错误分类与一次性副作用。keepalive 先释放 converter 已生成的 held message_start，再发 ping，避免内部 MessageStartSent 与实际 wire 提交状态混淆。原生 400 保留 envelope/额外字段，但 error.message 中的敏感 URL 参数继续脱敏。显式 passthrough source filter 阻止默认目录补入。

额度与生命周期裁决：手动 OpenAI quota 查询失败继续返回错误；自动刷新失败保留旧观测和 quota_refresh_failed，真正被动页面读取由 buildPassiveOpenAIUsage 承接，不访问上游。成功持久化快照才标 active；无上游窗口但有本地账单时保持 unknown 配合 WindowStats。上游周窗断言改为提供尚未到 reset 的观测；TokenKey 仍清理缺失/已过 reset 的陈旧周窗。Scheduler retirement race 继续验证 token 捕获、retire/reopen 与发布隔离，只把 mock 的阻塞点接到实际 ListModelAvailabilityCandidates owner。Vertex global-only 能力继续优先于账号地域默认值。

上游新增的 dashboard 请求日志/去重清理日界测试不适用于 TokenKey：DashboardAggregation 只清汇总，OpsCleanup 才拥有 runtime request retention；billing dedup 维持已审批 7d hot / 30d total，不随 usage retention 扩成 90/365 天。替代回归 TestDashboardAggregationRetentionLeavesRequestAndDedupCleanupToOpsOwner 同时执行两个 owner，验证 dashboard 不启动竞争删除、OpsCleanup 按运行期设置清 usage 且 dedup 仍独立。DST 日界的新测试继续保留。

配额与供应源边界复核：本地 WindowStats 仅附加到真实存在的 5h 窗口，缺失/过期窗口不生成 5h 行；7d 可保留 unknown 本地统计。Spark 的 additional_rate_limits 若没有 primary/secondary window，不写新快照或 active 标记。Bedrock 的 AWS 429 按模型冷却，模型未知时复用秒级账号兜底；不进入 Anthropic 无权威响应头规则与健康熔断，临时限流不永久停号。
