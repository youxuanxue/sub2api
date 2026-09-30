---
title: TokenKey upstream merge 2026-09-30 approval anchor
status: approved
approved_by: "用户（本会话批准，重点关注上游对 TokenKey 特性的修改、删除）"
approved_at: 2026-09-30
created: 2026-09-30
owners: [tk-platform]
scope: "Isolated import of Wei-Shaw/sub2api upstream/main; no main merge or deployment"
---

# TokenKey upstream merge 2026-09-30

固定 fork 基线为 `97c5d6a42e8c59205bbd57c2188ad8d6bf0220d7`，上游目标为
`96f4c115c9749078f90cbf210a01d39baf3f53b6`，共同祖先为
`9a62841fd124d026cf3694fcf9b79e98addcdbdc`。批准范围是隔离合并与验证，不授权
合入主线、运行时模型激活、生产配置变更或部署。

## TokenKey 不变量裁决

- 上游本轮无文件删除；审核同时覆盖无冲突的语义覆盖与旧实现复活风险。
- 实际账号与 `protocolrouter.Plan` 继续裁决执行资格；legacy WebSocket composite
  路由走既有 resolver，candidate 请求不根据计费组平台重新路由。
- TokenKey pricing registry 保持定价唯一来源。上游传感器数值不覆盖 active registry，
  模型识别和生成 bundle 不代表运行时激活。保留 GPT 别名归一化与 GPT-6.1 的
  TokenKey 上下文窗口；新官方客户端元数据的额外字段保留。
- 持久化 TokenKey hold 不再叠加 Redis 在途预留；native candidate 的 Redis 预留由
  candidate billing hook 在选号/计费来源变更时重建，异步结算引用按原生命周期释放。
- QA Bundle、脱敏、真实流 terminal 与工具参数保真继续沿用既有 owner；缺失
  `message_stop` 不伪造完成，缺失上游 usage 不伪造 Chat usage。
- UseKeyModal、AccountUsageCell、SettingsView 保持编排层，未恢复旧单体实现。
  Claude reset credits、风险白名单和 Codex remote/file catalog 迁入共享组件与 composable。
- 用户趋势 metric 复用 dashboard snapshot loader、序列守卫与 cache key，保留
  TokenKey day-rollup 查询路径。cyber session block 默认开启，设置读取故障仍 fail-open。
- 复审补强：Responses 重建协议上下文时保留在途预留句柄，异步计费完成前不释放；
  GPT-6.1 Sol 快捷映射保留 TokenKey 原预设且只显示一次。Claude reset 的查询、
  profile 与 claim 复用账号 TLS profile 和 canonical UA owner，保留禁止重定向、
  public-host 校验与 25 秒请求上限；TLS 失败不降级到普通出口。
- 上线前降风险：新增 Redis 在途预留默认关闭，显式启用后仍执行完整生命周期；
  TokenKey 原有持久化 hold 不关闭。AG 首内容前的保活及两分钟首内容截止时间同样
  默认关闭，复用共享配置 owner 显式启用，默认保留未提交响应的失败切换行为。
  手机首页只收紧共享 landing 组件的间距，桌面布局与产品文案不变。
- 生产只读评估与未决业务门禁见
  [上线前评估](../ops/upstream-pr2409-predeploy-20260930.md)。不因评估而授权生产配置、
  reset grant 兑换、新模型激活或部署；Astra ultrafast 定价变化须另行业务确认。

## 验证边界

验收运行 Go unit、真实 PostgreSQL/Redis integration、前端 lint/typecheck/unit/build 与
本地 fixture-backed Playwright UI；不使用生产凭据或付费上游。测试结果与残余覆盖以
本次会话/PR 实际输出为准。本审批文档不宣称已提交、已合入、已发布或线上验收。

最终历史审计使用固定两端 SHA；PR cadence 保留 `upstream/main..HEAD` 及脚本生成的
变更摘要，不通过 squash/rebase 丢弃上游 ancestry。
