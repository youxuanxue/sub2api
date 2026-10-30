---
title: Antigravity Argon 与 Flash 配额冷却范围
status: archived
archived_reason: "Unapproved proposal retained as historical evidence; implementation remains unapproved."
---

# 已归档：Antigravity 配额冷却范围

本文件是 #8393 的历史待审方案，尚未实现或部署。菜单是否展示不决定模型能否经 `model_mapping` 访问；本方案不改菜单、mapping、账号启停或价格。

## 已核实与缺口

2026-10-30 07:21–07:22 UTC（北京时间 15:21–15:22），prod 使用现有账号 usage 查询取得以下上游快照：

| 账号 | 上游 tier | Argon high 已用 / 重置 UTC | Flash high 已用 / 重置 UTC |
|---|---|---|---|
| 233 | PRO / g1-pro-tier | 0% / 11-03 15:32:54 | 0% / 10-30 12:21:56 |
| 294 | PRO / g1-pro-tier | 2% / 11-03 03:51:00 | 0% / 10-30 12:21:59 |
| 275 | FREE / free-tier | 未列出 | 0% / 11-06 07:21:58 |

PRO 样本的 Argon low/medium/high 重置时间一致，Argon 与 Flash 的窗口不同。这支持在有新鲜证据的 PRO 账号上区分两者；尚无 ULTRA 对照，也不能由这次快照反推历史 tier 或断言历史每次跳号都是误伤。线上旧快照曾同时记录 Argon 与 `antigravity:gemini` 冷却，检查时都已过期。

当前 `AntigravityQuotaFetcher` 返回逐模型额度与 tier，`getAntigravityUsage` 只缓存 UsageInfo；调度账号快照没有这份证据。`model_rate_limit.go` 对所有非图片 Gemini 同时写/读精确模型及 `antigravity:gemini`，因此确实存在跨模型阻断路径。

## 推荐契约

1. 在现有 quota fetch 成功路径记录版本化的账号配额证据：真实查询时间、raw/normalized tier、Argon 与 Flash 的额度窗口。仅完整成功快照可开启拆分；过期、缺字段、未知 tier、查询失败及来源不明均保持共享冷却。不能仅看账号名称或手工填入 `PRO`。
2. **新鲜度建议为 5 分钟**，从实际成功上游采样时间计时，不因读缓存续期；未来时间戳拒绝。它覆盖现有成功缓存 3 分钟并留短暂刷新余量。复用现有 quota 刷新 owner，不在选号热路径同步查询上游，也不新增无界后台刷新。持久化/分发须阻止旧快照覆盖新快照；具体存储 owner 与调度快照失效机制在实现前确认。
3. 一个 resolver 同时服务冷却写入、候选判断、剩余时间与原因诊断。输入为账号、新鲜证据、最终解析后的上游模型及限流类别；计费组、菜单名称和展示别名不参与裁决。
4. 仅在已确认 PRO 且样本仍支持独立窗口时，对**模型配额类 429** 拆分：Argon 各变体写精确模型 + `antigravity:gemini:argon`，读同一子家族；非 Argon Gemini 保持精确模型 + 原 `antigravity:gemini`。不能只写 exact key，让 low/medium/high 互相绕过共同额度。
5. ULTRA、FREE、UNKNOWN 和缺失/过期证据保持原共享策略。拆分后证据失效或 tier 改变时，仍须保守读取尚有效的 Argon 子家族冷却，防止切换 tier/变体绕过旧冷却。旧 family 冷却没有来源信息时不得直接删掉或假定来自 Argon；让它自然到期。
6. 账号级额度/并发/容量限制继续由既有 owner 处理；不能把所有 429 当作模型额度。404 `antigravity_404_model_unavailable` 仍是精确模型能力冷却，不能用于推导额度池或触发 PRO 拆分。路由和模型授权保持原契约。

## 验证矩阵与上线观察

| 场景 | 必须成立 |
|---|---|
| 新鲜 PRO 独立窗口，Argon 配额耗尽 | 所有 Argon 变体停止选号，Flash 仍可选；读写与诊断 scope 一致 |
| 新鲜 PRO，Flash 配额耗尽 | Flash 共池冷却；Argon 可选，其他账号级限制仍生效 |
| ULTRA / FREE / UNKNOWN / 缺字段 / 过期 / 未来时间 | 旧共享冷却保持生效，旧 Argon 子家族不能被绕过 |
| 同一账号 tier 变化、快照乱序或刷新失败 | 不清除已有冷却；旧数据不能覆盖新数据；到期后保守回退 |
| alias、low/medium/high、native thinking 变体 | 使用实际 final model；同额度池变体不能相互绕过 |
| 404 unavailable、全账号额度、短时容量 429 | 继续原能力/账号/容量策略，不误归类为独立模型额度 |
| 菜单隐藏、mapping 保留 | 允许按已有 mapping 访问，冷却不创建新的模型禁用规则 |

先以离线 fixture 和真实 Forward/候选测试验证以上矩阵，再用有新鲜 PRO 证据的受控账号验证“Argon 已用尽且 Flash 仍有量”。不主动耗尽账号额度。部署后记录实际 scope、tier 证据年龄和跳号原因，观察 Flash 可用池是否恢复以及上游 429 是否反弹。回退关闭拆分策略，仍识别已有子家族冷却直到自然到期。生产修改另行按发布流程执行。
