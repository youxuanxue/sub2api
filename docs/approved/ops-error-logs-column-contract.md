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
`ALLOWED_UNWRITTEN`,每条必须写原因;当前 3 条,全部是「列还声明着、写入方已摘」的两阶段
删除中间态(见下节),没有一条是「以后有空再补写入方」。

tk_100 的六列**不在**例外表里:门禁的 `declared` 来自迁移文件,而 tk_100 声明了 DROP,这六
列本就不在声明集内,写进例外表只会读成一层不存在的覆盖。它们在 prod 仍然物理存在(tk_100
只记录不执行),留下的拦截缺口记在
[`docs/preflight-debt.md`](../preflight-debt.md)。

## Owners

| 关注点 | Owner |
| --- | --- |
| 列声明 | `backend/migrations/*.sql`(`ops_error_logs` 相关) |
| 落库写入 | `backend/internal/repository/ops_repo.go`(`insertOpsErrorLogSQL` + `opsInsertErrorLogArgs`) |
| user-visible failure 判据 / SLA 分子 | `backend/internal/repository/ops_repo_user_visible_failure_tk.go` |
| 契约门禁 | `scripts/checks/ops-error-log-column-writers.py` |
| 「归因已移除」的反向哨兵 | `scripts/sentinels/gateway-tk.json`:`ops_error_logger.go` / `ops_repo_user_visible_failure_tk.go` 的 `must_not_contain`,`migrations_runner.go` 的 tk_100 只记录分支 |

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
| 阶段 1(本次) | 摘掉写入方、所有读取方、明文审计写入、相关测试;三列仍保持声明 | 本 PR |
| 阶段 2(回滚窗口关闭后的下个版本) | 跑上游已有的 `backend/scripts/finalize-ingress-reject-cleanup.sql`(DROP 三列 + `DROP TABLE deleted_api_key_audits`);tk_100 的六列它**不覆盖**,需另写 DDL。同时删掉 `ALLOWED_UNWRITTEN` 的 3 条、tk_100 只记录分支与 `docs/preflight-debt.md` 的两条 | 待办 |

**阶段 2 不需要新写 SQL**:上游的 finalizer 已经在树内
(`backend/scripts/finalize-ingress-reject-cleanup.sql`),删的正是这三列加明文审计表,
并带 `lock_timeout = 5s`;配套的历史行清理工具是 `backend/cmd/cleanup-ingress-reject-logs`
(默认 dry-run,`--execute` 才真删)。它的前置条件与本 PR 的阶段划分一致:所有实例都升到
「不再读写这些列」的版本之后才能跑。唯一它没覆盖的是 tk_100 那六列 —— 那是 TK 自己的,
要单独一条 DDL。

阶段 1 不删列的理由与 tk_100 相同:迁移在新 color 启动时执行,旧 color 还在读同一个库,
上一版本的 SQL 仍然会点名这三列;列一删,blue/green 窗口和镜像回滚同时被打断。列没有写入
方,推迟物理删除不产生任何数据代价。

`DeleteWithAudit` 的函数名保留上游原样(缩小 diff),但内部只做 tombstone 软删除,不再写
`deleted_api_key_audits`;表在阶段 2 一起 DROP。

## 已批准的删列(tk_100,当前「只记录不执行」)

`backend/migrations/tk_100_ops_error_logs_drop_unwritten_columns.sql` 声明删除 6 个从未有
写入方的列。

**当前状态(2026-09-27 用户选择)**:tk_100 **只记录不执行** ——
`migrations_runner.go` 的 `shouldRecordMigrationWithoutExecution` 对
`migrations.RetainedOpsErrorColumnsMigration` 返回 true,只往 `schema_migrations` 插一行,
不跑 SQL。迁移文件保持不变(文件不可变 / checksum 契约),所以下面这张表描述的是**文件声明**
的意图,六列在 prod 仍然物理存在。物理删除与三列归因一起推到阶段 2;这期间门禁看不见这六
列(既不在声明集、也不会在运行时炸),缺口记在 `docs/preflight-debt.md`。

理由与上面同构:`DROP COLUMN` 在活跃分区树上递归加 93 把 `ACCESS EXCLUSIVE`,并删掉旧
color 的 SQL 仍然点名的列,blue/green 窗口与镜像回滚同时被打断;而这六列没有写入方,推迟
物理删除零代价、执行却不可逆。下面「blue/green 窗口」一节记录的是**如果执行**的风险面,
保留它是为了阶段 2 直接复用这份核账,而不是重新分析一遍。

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
