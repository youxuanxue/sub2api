---
title: TokenKey upstream merge 2026-11-11 isolation and migration scope
status: pending
approved_by: pending
authors: [codex]
created: 2026-11-11
---

# 2026-11-11 上游合并范围

用户在本会话要求继续执行 `$tokenkey-upstream-merge`。该指令及后续“提交 pr 里面”授权在隔离分支完成合并实现、冲突裁决、本地验证、提交推送和独立 PR；本文不声称用户已审批生产迁移、部署或主线合并。生产发布前由人确认本节迁移和保留行为。

初始审计 fork base 为 `77f6f14977222171b5aba41ae99b8d078f01c42c`；用户要求对齐 origin/main 后，实际合并基线更新为 `8a375b68b6cc107cbc36ea086a20b2fbdb52d6a0`（包含 #3149 和 #3150）。上游目标仍为 `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484`；保留双 parent merge ancestry。

## 数据库增量

- `249_add_usage_logs_api_key_cost_index_notx.sql`：为 `usage_logs (api_key_id, created_at)` 添加 `INCLUDE (actual_cost)` 覆盖索引；分区表复用现有逐子表 concurrent 创建、父索引 attach 与可重试 owner，避免直接对分区父表执行不支持的 concurrent DDL。
- `250_openai_cache_write_ttl.sql`：usage 增加默认零的 30m/12h cache-write token 列，渠道与账号统计价卡及区间增加可空的 12h 单价。NULL 保持既有通用 cache-write 单价；显式零保持免费。
- 编号按完整文件名记账。既有 `249_mcp_oauth.sql` 保持原名与内容；不改历史 migration checksum。
- 本地集成验证使用隔离容器，不 apply 线上数据库。新增字段部署后的锁等待、索引磁盘与 I/O 成本需在发布窗口观察；回滚应用不删除已新增列和索引。

## 冲突裁决与兼容边界

- 保留 candidate eligibility、protocol Plan、客户扣费与 response cost preview、共享 UI owner、QA raw capture 等既有 SSOT；上游新增意图迁入 owner。
- 保留 Anthropic 未知 cache TTL 归入 1h 的已批准语义；OpenAI 30m/12h 为独立维度，不能混入 Anthropic 5m/1h。
- 保留模型级 transient/capacity 冷却，避免单模型 500/503 扩大为账号停用；显式 temp-unsched scope 与凭据级 401 优先级按上游语义接入。
- 保留 TokenKey 并发 refresh 的既有短宽限期。上游新增 atomic consume cache 能力仍保留，但未替代该流程；已轮转 token 在宽限期内的重放窗口属于既有安全取舍，不将其标为已获得上游单次消费防护。
- Codex 接入指南保留标准 auth.json 默认，提供上游 API-key native image 模式选择；动态目录继续决定可选模型，新增元数据不代表线上模型已激活。
- 工具结果媒体继续嵌入各自 functionResponse；legacy model ID 不使用上游的 sibling fallback，避免多工具图片归属错配。
- 保留已删除 `deploy/EDGE_SECURITY.md`；现网运维入口仍为 `deploy/hetzner/README.md`。

## 验证与发布判断

运行结果与未覆盖项记录在本轮审计报告和 PR。编译、后端 unit/integration、前端 lint/typecheck/build、真实 Playwright UI 和 preflight 全部完成后才提交。任何未完成项不得写成通过。
