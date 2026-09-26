---
title: ops_error_logs 列契约（声明 == 写入 ∪ DB 托管）
status: approved
approved_by: "feng (2026-09-26 conversation: 针对 6 个无写入方存量列的不可逆 DDL，明确选择「保留迁移，本 PR 一起 DROP」)"
approved_at: 2026-09-26
created: 2026-09-26
authors: [claude]
risk: high
related_prs: ["#2349"]
---

# ops_error_logs 列契约(approved)

`ops_error_logs` 没有 Ent schema:列由 `backend/migrations` 的裸 DDL 声明,只有
`backend/internal/repository/ops_repo.go` 一条手写 INSERT 和一条 UPDATE 落库。两边没有
任何机械约束,所以一个列可以被迁移声明、被读取侧使用、却从来没有写入方——每行都是
NULL,而外面一层 `COALESCE` 会安静地退到 fallback,让空列看起来像有数据。

## 契约

**声明(migrations)== 写入(ops_repo.go)∪ DB 托管(id, created_at)。**

由 `scripts/checks/ops-error-log-column-writers.py` 在 preflight 强制,两侧真值都在运行
时推导、不手工维护:

- `declared` = `CREATE TABLE` 正文 + `ADD COLUMN` − `DROP COLUMN`(全量 migrations)
- `written` = INSERT 列清单 ∪ `UPDATE ... SET` 赋值列(非测试 Go)

双向拦截:声明未写入(接写入方或 DROP)、写入未声明(运行时会炸)。例外走
`ALLOWED_UNWRITTEN`,每条必须写原因;当前为空,契约精确。

## Owners

| 关注点 | Owner |
| --- | --- |
| 列声明 | `backend/migrations/*.sql`(`ops_error_logs` 相关) |
| 落库写入 | `backend/internal/repository/ops_repo.go`(`insertOpsErrorLogSQL` + `opsInsertErrorLogArgs`) |
| 已删除 key 归因写入 | `backend/internal/handler/ops_error_logger.go` 的 `INVALID_API_KEY` 分支 |
| user-visible failure 判据 | `backend/internal/repository/ops_repo_user_visible_failure_tk.go` |
| 契约门禁 | `scripts/checks/ops-error-log-column-writers.py` |

## 已批准的删列(tk_100)

`backend/migrations/tk_100_ops_error_logs_drop_unwritten_columns.sql` 删除 6 个从未有写
入方的列:

| 列 | 声明来源 | 等价的、有写入方的替代 |
| --- | --- | --- |
| `duration_ms` | 033 | `response_latency_ms` |
| `network_error_type` | 033 | `error_type` / `error_owner` |
| `provider_error_code` | 033 | `upstream_status_code` + `upstream_error_message` |
| `provider_error_type` | 033 | `error_type` |
| `account_status` | 033 | 关联 `accounts.status`(语义是「当前」,不是「失败时」) |
| `retry_after_seconds` | 033 | 无(重试/回放存储已由 136 移除,本列是残留) |

**安全性依据**:prod 全时段实测 2,213,627 行,上述六列 `count(<col>)` 均为 0,删除不丢
任何数据。读取侧已在同一 PR 全部改到有写入方的等价列,并经 prod 实跑验证(probe-caps、
probe-ops-error-request-shape、probe-user-billing-watch 均无 SQL 错误且字段有真值)。

**保留 vs 删除**:按硬规则 §5.x 默认保留上游功能。这里选择删除,因为这些列没有行为、
没有开关、没有任何可以被打开的写入路径——留着只会让声明与现实继续分叉,让下一个读它
的人再被骗一次(已实际发生:盯盘探针把 `provider_error_code` / `network_error_type` 当
根因字段展示,ops 请求详情页把 `duration_ms` 当耗时展示,全都是空的)。

## 为什么不用 Ent schema

字面上的根因修法是让 Ent schema 成为唯一真值来源。这里不这么做:`ops_error_logs` 是分
区表,写入在网关热路径上走裸 SQL 批量落库,套 Ent 会同时改变分区路由和写入性能特征。
契约校验达成同一个目标(声明/写入/读取不可能静默分叉),且不动热路径。若将来该表脱离
热路径,Ent 化仍是更彻底的选项。

## 已知边界

见 [`docs/preflight-debt.md`](../preflight-debt.md#known-limits-of-the-column-writer-gate):
Go 侧裸列名不做表归属判定(由契约结构性覆盖)、门禁只验「有没有写入方」不验「值对不对」、
目前只覆盖 `ops_error_logs` 一张表。
