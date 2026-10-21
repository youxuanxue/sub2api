# Codex 额度刷新与账号限流反馈修复

## Background

2026-10-21 prod 1.8.311，账号 283 的截图仍显示周额度 100%，随后 live 库与
`/backend-api/wham/usage` 已为 1%，`allowed=true`。缺少截图拍摄时刻的快照，
不能据此断言上游在截图当时就已恢复。代码中可复现的刷新断点为：

- 列表被动加载不查询上游，但返回列表构建时间作为快照更新时间；没有按采样年龄标记未知。
- 普通 HTTP OAuth 的过期检测受 WSv2 开关限制，所有当前原生账号都关闭 WSv2。
- 主动查询发起模型请求并读取 quota 响应头；失败或缺头时静默返回旧快照。
- quota 卡刷新仅保存点数/重置次数，没有保存同一响应中的额度窗口；reset 返回新账号行也没有刷新被列表 override 接管的进度条。
- 主动查询未更新前端缓存，较早的批量响应也能覆盖刚查到的值。

同日 11:56–11:59 UTC，账号 283 的上游事件包含 920 次 429（含重试）。
响应为 `{"detail":"Rate limit exceeded"}`。原有原生账号滚动饱和计数只接收
明确的 503 容量错误；普通 429 进入两分钟同账号重试。同期 283 及其他账号的
503 计数失败为 `context canceled`，来自继承客户端取消的 Redis 写入。

## Delta

- 主动用量查询统一读取现有 OpenAIQuotaService 的 quota endpoint；保留 Spark 维度隔离、代理和凭据 owner。
- 查询成功先保存窗口再响应；查询失败可见。被动列表不产生上游请求，使用真实采样时间，过期百分比标记未知。
- 完整 quota 响应中的 null 窗口清除旧窗口；quota 卡点数刷新同时保存正确维度的窗口，不依赖重置次数详情是否完整。
- 主动查询/重置/次数查询均更新使用中的进度条；旧批量响应不能覆盖更新的采样。
- 原生 OpenAI OAuth/SetupToken 的明确普通账号级 `Rate limit exceeded` 接入现有通用候选失败 owner：按账号和 Plan 的实际上游模型计数，固定 90 秒窗口内达到 3 次后，有效 priority 增加 1000；不写入容量饱和计数，不新增硬停调。
- 该类错误立即进入其他账号的既有有界 failover，不再执行同账号密集重试；额度耗尽、Spark/图片限额、API-key 和不明错误保持各自 owner。
- 容量反馈保存使用现有有界、脱离客户端取消的状态更新上下文。

## Scenarios / Validation

- 后端 `TestOpenAIUsageRefreshReplacesExhaustedSnapshotWithoutInference`：HTTP 账号旧 100% → quota GET 新 1%、null 窗口移除、同步持久化、失败显式返回、列表不发上游请求。
- `TestQuotaCardRefreshPersistsWindowsWithoutResetCreditDetails`：窗口保存不依赖重置次数详情。
- `TestCandidateFailureCodex429UsesUnifiedPriority`：直连和通用 key 经真实执行边界反馈，达到既有阈值后选号避让、模型隔离、同一执行不双计数、过期恢复。
- `TestOpenAIAccount429PressureSwitchesWithoutSaturationPenalty`：首次切号且无重复容量惩罚，排除 quota/Spark/API-key/回显输入。
- `TestOpenAISaturationSurvivesClientCancellation`：已取消请求仍保存一次反馈。
- `AccountUsageCell.spec.ts`：主动查询与旧批量响应竞态、reset/次数查询更新 override 行、失败提示。
- `playwright.codex-quota.config.ts`：真实 Accounts UI 点击查询，100% → 1%，重载保留，失败提示。

未部署前，线上行为仍以现有版本为准。
