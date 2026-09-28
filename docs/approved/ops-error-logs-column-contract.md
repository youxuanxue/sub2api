---
title: ops_error_logs 列契约（声明 == 写入 ∪ DB 托管）
status: approved
approved_by: "feng (2026-09-26 conversation: 针对 6 个无写入方存量列的不可逆 DDL，明确选择「保留迁移，本 PR 一起 DROP」)；feng (2026-09-27 conversation: 「同意转向上游方向。完全没必要为这事单独和上游分叉，完全没有什么正向收益。」——移除已删除 key 归因，并选择「两阶段：先摘读写，下个版本再删列」+「tk_100 改成只记录不执行」)"
approved_at: 2026-09-27
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

归因相关的两个 owner 已随功能一起删除(`ops_error_logger.go` 的 `INVALID_API_KEY` 分支、
`ops_error_logger_attribution_test.go`),原因见下节。

## 已移除的已删除 key 归因

migration 145 声明了 `attempted_key_prefix` / `deleted_key_owner_user_id` /
`deleted_key_name` 三列,配套还有 `deleted_api_key_audits`(明文 key 审计表)。2026-09-26
一度按「上游合并静默丢了写入方」把写入方恢复回来,2026-09-27 实测后判定为**功能本身不该
存在**,整条链路(写入方 + 读取方 + 明文审计写入 + 回归测试)一并移除。

判定依据(prod 只读实测,2026-09-27,`i-0e43099f831b03160`):

- **写入方物理上到不了**:`INVALID_API_KEY` 由认证中间件打 ingress-reject 标记,
  `OpsErrorLoggerMiddleware` 在归因块**上方**就对该标记早退,请求根本走不到归因分支。
  本地加临时用例验证:按 `api_key_auth.go` 的方式打标记后落库队列长度为 0。原先 4 条测试
  能过,是因为它们绕开了认证链直接调中间件。
- **存量数据为 0**:2,212,025 行里三列 non-null 计数全为 0。
- **分母本身就是噪声**:30 天 `ops_ingress_reject_aggregates` 里 `invalid_api_key` 6,009 次
  /72 个 IP(约占错误行 0.27%),按用户拆分是 anonymous 5,889 / known_user **0** —— 是扫描
  流量,不是「用户的 key 被删了」。同期 401 `authentication_error` 只有 41 行,且全部已带
  `user_id`。7 天 SLA 分子 1,048,914 行 / 7 个用户,归因量级不可见。
- **保留期本身矛盾**:错误日志 30 天到期(`data_lifecycle_policy_tk.go` 封顶 30 天),而
  `deleted_api_key_audits` 没有任何清理(无界),即「永久保存明文 key」最多换来 30 天的可见
  归因。
- **上游方向一致**:上游 `cleanup-ingress-reject-logs` + `finalize-ingress-reject-cleanup.sql`
  的既定方向就是删掉这三列和明文 key 审计表。

按硬规则 §5.x「默认保留上游功能」,这里是**经批准的例外**:保留方向与上游相反,且保留的
代价是把提交上来的 key 前缀和用户自己起的 key 名长期写进 S3 遥测载荷。用户结论:
「完全没必要为这事单独和上游分叉,完全没有什么正向收益。」

**两阶段删除**(用户明确选择):

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| 阶段 1(#2353,v1.8.261) | 摘掉写入方、所有读取方、明文审计写入、相关测试;三列仍保持声明 | 已完成 |
| 阶段 2(tk_101) | `backend/migrations/tk_101_ops_error_logs_finalize_unwritten_columns.sql`:一次 DROP 九列(tk_100 六列 + 归因三列)+ `DROP TABLE deleted_api_key_audits`;同时清掉 `ALLOWED_UNWRITTEN` 三条、tk_100 只记录分支及其哨兵、`docs/preflight-debt.md` 两条 | 已完成 |

**阶段 2 为什么自己写 SQL 而不是直接跑上游 finalizer**:上游的
`backend/scripts/finalize-ingress-reject-cleanup.sql` 只删归因三列加明文审计表,不覆盖
tk_100 那六列;而两组列的前置条件、风险面完全相同,分两次执行只会把 92 个分区的
`ACCESS EXCLUSIVE` 锁获取做两遍。tk_101 合并两组,内容是两者的并集,`lock_timeout = 5s`
沿用上游。上游那份脚本作为参照留在树内,不再是执行路径。配套的历史行清理工具
`backend/cmd/cleanup-ingress-reject-logs` 清的是行不是列,不阻塞删列;它在 TK 没有交付路径
(不在镜像、不在 goreleaser),记在 `docs/preflight-debt.md`。

阶段 1 不删列的理由与 tk_100 相同:迁移在新 color 启动时执行,旧 color 还在读同一个库,
上一版本的 SQL 仍然会点名这三列;列一删,blue/green 窗口和镜像回滚同时被打断。列没有写入
方,推迟物理删除不产生任何数据代价。

`DeleteWithTombstone` 内部只做 tombstone 软删除,不写 `deleted_api_key_audits`;对外方法名
`DeleteWithAudit` 保留上游原样(滚动升级兼容 + 缩小 diff)。表已随 tk_101 DROP。

## 已批准的删列(tk_100 声明,tk_101 执行)

`backend/migrations/tk_100_ops_error_logs_drop_unwritten_columns.sql` 声明删除 6 个从未有
写入方的列。

**tk_100 始终没有执行过**:`migrations_runner.go` 曾对它返回 record-only,只往
`schema_migrations` 插一行。它在每个库都已登记、checksum 已冻结、文件不可变,所以**永远不会
再执行** —— 六列的物理删除只能由 tk_101 接手,record-only 分支和常量随之删除(留在原处反而
会在全新库上把 DDL 重新武装起来)。tk_100 末尾那条 `COMMENT ON TABLE` 同样从未生效,也由
tk_101 补上。

反向不变式已钉住:tk_101 若被加进 `shouldRecordMigrationWithoutExecution`,列会永久留在库
里而门禁照样 PASS(门禁读 DDL,不读数据库),所以
`migrations_runner_ops_error_columns_finalizer_test.go` 断言它必须执行。

当初推迟的理由:`DROP COLUMN` 在活跃分区树上递归加 93 把 `ACCESS EXCLUSIVE`(父表 + 92 个
分区),并删掉旧 color 的 SQL 仍然点名的列,blue/green 窗口与镜像回滚同时被打断;而这六列
没有写入方,推迟物理删除零代价、执行却不可逆。下面「blue/green 窗口」一节的核账在 tk_101
直接复用,没有重新分析。

**tk_101 执行时接受的代价**:全 fleet(prod + us3/us4/us5/us6,每个 Edge 各有独立 Postgres)
已在 2026-09-27 全部升到 v1.8.261,该版本对九列既不读也不写;但删列之后**回滚到 1.8.260 会
42703**,因为 1.8.260 的 `ops_repo_user_visible_failure_tk.go` 与
`ops_repo_routing_capacity_tk.go` 仍在 SQL 里点名归因三列。这是明确接受的取舍,前提是
1.8.261 两阶段 post-check 双 green。迁移由各库自己的启动 runner 驱动,五个库无需手工逐库操作。

**库内核对**(门禁只读 DDL,读不到五个独立 Postgres):用
`ops/observability/probe-migration-applied-verify.sh` 经 `run-probe.sh` 逐 host 查
`schema_migrations` + catalog。默认参数覆盖 tk_100/tk_101;换 `MIGRATION_FILENAMES` /
`DROPPED_COLUMNS` / `PARENT_TABLE` 可复用于同类 finalize。

**删列后的 VACUUM**:`ops/observability/vacuum-ops-error-logs.sh`(`EXECUTE_VACUUM=1`)
跑 `VACUUM (ANALYZE) ops_error_logs`。刷新统计、清普通死元组;不强制重写表(缩盘要
`VACUUM FULL` / `pg_repack`,另排窗口)。2026-09-28 已在 prod + us3/us4/us5/us6 执行完毕。

文件声明删除的六列:

| 列 | 声明来源 | 等价的、有写入方的替代 |
| --- | --- | --- |
| `duration_ms` | 033 | `response_latency_ms` |
| `network_error_type` | 033 | `error_type` / `error_owner` |
| `provider_error_code` | 033 | `upstream_status_code` + `upstream_error_message` |
| `provider_error_type` | 033 | `error_type` |
| `account_status` | 033 | 关联 `accounts.status`(语义是「当前」,不是「失败时」) |
| `retry_after_seconds` | 033 | 无(重试/回放存储已由 136 移除,本列是残留) |

**安全性依据**(prod 只读实测,2026-09-26):全时段 2,213,666 行,上述六列 `count(<col>)`
均为 0,删除不丢任何数据。六列上没有任何索引,也没有视图/物化视图依赖本表(视图依赖会直接
阻塞 `DROP COLUMN`)。读取侧已在同一 PR 全部改到有写入方的等价列,并经 prod 实跑验证
(probe-caps、probe-ops-error-request-shape、probe-user-billing-watch 均无 SQL 错误且字段
有真值)。

**blue/green 窗口**:本表是分区表(`relkind='p'`,92 个分区),`DROP COLUMN` 递归加锁,需要
父表 + 92 个分区共 93 把 `ACCESS EXCLUSIVE`;只改 catalog、不重写表。迁移在新 color 启动时
执行,此时旧 color 仍在服务同一个库,因此逐条核过旧代码:热路径 INSERT 的列清单不含这六列
(它们本就没有写入方),唯一的 `UPDATE` 只 SET `resolved*`,本表不在 `ent/migrate/schema.go`
中(裸 DDL 表,没有 ent 侧全列 SELECT)。窗口内唯一影响面是旧 color 上 admin 只读的「请求
详情」列表(`ListRequestDetails`)会返回 42703——该页面在删除前对错误行本来就只显示空耗时、
且按耗时筛选会把错误行全部排除,即旧代码里它已经是坏的;它不在客户流量路径上,不影响计费、
调度或 SLA 分子,切流后即恢复且首次真正可用。迁移设 `lock_timeout = 5s`(逐次加锁计时),
抢不到锁即整条回滚,宁可迁移失败也不在热路径 INSERT 前形成锁队列;失败是安全的,重跑即可
(`DROP COLUMN IF EXISTS` 幂等)。

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
