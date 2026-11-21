---
title: ops_error_logs 列契约（声明 == 写入 ∪ DB 托管）
status: approved
approved_by: "feng (2026-09-26 conversation: 针对 6 个无写入方存量列的不可逆 DDL，明确选择「保留迁移，本 PR 一起 DROP」)；feng (2026-09-27 conversation: 「同意转向上游方向。完全没必要为这事单独和上游分叉，完全没有什么正向收益。」——移除已删除 key 归因，并选择「两阶段：先摘读写，下个版本再删列」+「tk_100 改成只记录不执行」)"
approved_at: 2026-09-27
created: 2026-09-26
authors: [claude]
risk: high
related_prs: ["#2349", "#3382"]
revised: 2026-11-21（#3382：真实用户 Feishu/SLA 分子排除 users.role=admin 运维探针；保留契约与 tk_101 边界）
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
`ALLOWED_UNWRITTEN`,每条必须写原因;**阶段 2(tk_101)之后该表为空**,门禁报
`contract exact`。三条归因例外的唯一用途是让列在写入方摘除后多活一个 blue/green 窗口,
tk_101 把列和例外一起收了。

阶段 2 之后 prod 物理列 = 迁移声明列,不再有「声明集外仍然存在」的列。tk_101 生效前
tk_100 那六列属于这一类,相应的拦截缺口已从
[`docs/preflight-debt.md`](../preflight-debt.md) 关闭。

## Owners

| 关注点 | Owner |
| --- | --- |
| 列声明 | `backend/migrations/*.sql`(`ops_error_logs` 相关) |
| 落库写入 | `backend/internal/repository/ops_repo.go`(`insertOpsErrorLogSQL` + `opsInsertErrorLogArgs`) |
| user-visible failure 判据 / SLA 分子 | `backend/internal/repository/ops_repo_user_visible_failure_tk.go` |
| 契约门禁 | `scripts/checks/ops-error-log-column-writers.py` |
| 「归因已移除」的反向哨兵 | `scripts/sentinels/gateway-tk.json`:`ops_error_logger.go` / `ops_repo_user_visible_failure_tk.go` 的 `must_not_contain` |
| 「finalizer 必须真执行」 | `migrations_runner_ops_error_columns_finalizer_test.go`:tk_101 不得被加进 `shouldRecordMigrationWithoutExecution` |

**真实用户 Feishu 分子（#3382）**：`CountUserVisibleFailures` /
`buildUserVisibleFailureWhere` 只统计可归属的客户请求。`users.role=admin`
是运维/探针账号（prod 例：`#1 admin@tokenkey.dev` 经 key `tk` 打已下架模型），
其终态失败**保留**在 raw `ops_error_logs`，但**不得**进入
`user_visible_failure_count` / `client_visible_failure_count` 告警分子与卡片
breakdown。规则描述由 `tk_115_exclude_admin_from_user_visible_alerts.sql` 对齐。

归因相关 owner（`INVALID_API_KEY` 写入分支、明文 `deleted_api_key_audits`）已按批准
例外移除；§5.x 默认可保留上游功能，此处用户明确选择不与上游为归因分叉。

## 已合入删列边界（tk_100 / tk_101）

- **阶段 1**：摘读写与测试，列暂留声明（blue/green 旧 color 仍可能点名列）。
- **阶段 2（tk_101，已执行）**：一次 `DROP` 九列（tk_100 六列无写入方 + 归因三列）+
  `DROP TABLE deleted_api_key_audits`；清 `ALLOWED_UNWRITTEN`；tk_100 保持 record-only
  不可再执行。tk_101 **必须真执行**（finalizer 测试禁止加进
  `shouldRecordMigrationWithoutExecution`）。
- 无写入方六列及等价替代：`duration_ms`→`response_latency_ms`；
  `network_error_type`/`provider_error_type`→`error_type`（及 owner）；
  `provider_error_code`→`upstream_status_code`+`upstream_error_message`；
  `account_status`→关联 `accounts.status`；`retry_after_seconds` 无替代（残留）。
- 分区表 `DROP COLUMN`：`lock_timeout=5s`、幂等 `IF EXISTS`；核对用
  `probe-migration-applied-verify.sh`。`DeleteWithAudit` 名保留上游，实现仅 tombstone。

一次性 fleet 行数/VACUUM 执行回执已删。

## 为什么不用 Ent schema

分区热路径裸 SQL 批量写入；契约门禁达成声明/写入一致且不动性能特征。脱离热路径后再评估 Ent。

## 已知边界

见 [`docs/preflight-debt.md`](../preflight-debt.md#known-limits-of-the-column-writer-gate)。
