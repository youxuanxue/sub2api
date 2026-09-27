-- high-risk-anchor: docs/approved/ops-error-logs-column-contract.md
-- 清理 ops_error_logs 上「声明了但从来没有写入方」的列。
--
-- 这些列由 033_ops_monitoring_vnext.sql 声明,但至今没有任何写入方:INSERT 列清单里
-- 没有它们,也没有 UPDATE 赋值它们。后果不是「少一点数据」,而是读取侧会被误导——
-- 一层 COALESCE 套上去,空列会安静地退到 fallback,看起来像有数据。这已经实际发生
-- 过:盯盘探针把 provider_error_code / network_error_type 当根因字段对外展示,
-- ops 请求详情页把 duration_ms 当耗时展示,全都是空的。
--
-- 保留 vs 删除:按硬规则 §5.x「默认保留上游功能」,能留的都留。但这些列没有行为、
-- 没有开关、没有任何写入路径可以被打开——留着只是让「声明」和「现实」继续分叉,
-- 让下一个读它的人再被骗一次。所以这里删列,而不是加注释。
-- 等价的、有写入方的列已经存在,读取侧已在本 PR 一并改过去:
--   duration_ms          -> response_latency_ms(端到端耗时,INSERT 里有写)
--   network_error_type   -> error_type / error_owner(分类,INSERT 里有写)
--   provider_error_code  -> upstream_status_code + upstream_error_message
--   provider_error_type  -> error_type
--   account_status       -> 关联 accounts.status(且语义是「当前」而非「失败时」)
--   retry_after_seconds  -> 重试/回放存储已由 136 移除,本列是其残留
--
-- 之后由 scripts/checks/ops-error-log-column-writers.py 保证不再新增同类列:
-- 声明(migrations)/ 写入(ops_repo.go)/ 读取(ops + deploy + backend)三方不一致会 fail
-- preflight。
--
-- bluegreen-safe-destructive-ok: 窗口内旧 color 的写入路径完全不碰这六列,读取只有一处
-- 且是 admin 只读页面。逐条核过 origin/main:
--   * 热路径 INSERT(insertOpsErrorLogSQL)的列清单里没有这六列中的任何一个——它们本来
--     就没有写入方,这正是删除的理由。网关落库不受影响。
--   * ops_error_logs 上唯一的 UPDATE 只 SET resolved / resolved_at /
--     resolved_by_user_id。不受影响。
--   * 别名限定读取全仓库只有一处:ops_repo_request_details.go 的 o.duration_ms。
--     ops_repo_dashboard.go / ops_repo_preagg.go / ops_metrics_collector.go 里的裸
--     duration_ms 都是 FROM usage_logs,与本表无关(已逐个看 FROM 子句确认)。
--   * ops_error_logs 不在 ent/migrate/schema.go 里(裸 DDL 表),所以没有 ent 侧
--     SELECT 会枚举全部列。
-- 因此窗口内的唯一影响面:旧 color 上 admin「请求详情」列表(ListRequestDetails,
-- handler/admin/ops_handler.go)在切流前若被访问会返回 42703。这个页面在本次删除前
-- 对错误行本来就只显示空耗时、且按耗时筛选会把错误行全部排除,即它在旧代码里已经是坏
-- 的;它是管理员只读钻取页,不在客户流量路径上,不影响计费/调度/SLA 分子。新 color
-- 已改读 o.response_latency_ms(本 PR 同批),切流后即恢复且首次真正可用。
-- 前置校验(prod 实测,2026-09-26):relkind='p' 分区表、92 个分区、全时段 2,213,666 行,
-- 六列均 0 非空;六列上没有任何索引,也没有视图/物化视图依赖本表(视图依赖会直接阻塞
-- DROP COLUMN)。无数据丢失,无对象需要先行重建。
-- 锁:DROP COLUMN 只改 catalog、不重写表(不回收已有行的空间),但在分区表上会递归,需要
-- 父表 + 92 个分区共 93 把 ACCESS EXCLUSIVE 锁。lock_timeout=5s 是逐次加锁计时而非整条
-- 语句的总预算:任何一把锁 5s 内抢不到就整条事务放弃回滚,宁可迁移失败也不排在热路径
-- INSERT 前面形成锁队列。失败是安全的——这六列没有写入方,推迟删除不影响任何功能,重跑
-- 即可(DROP COLUMN IF EXISTS 幂等)。
-- 回滚:本列无写入方,回滚只需重新 ADD COLUMN(可空、无默认值、不回填),同样不重写表。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_error_logs
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS network_error_type,
    DROP COLUMN IF EXISTS provider_error_code,
    DROP COLUMN IF EXISTS provider_error_type,
    DROP COLUMN IF EXISTS account_status,
    DROP COLUMN IF EXISTS retry_after_seconds;

COMMENT ON TABLE ops_error_logs IS 'Ops error logs (vNext). Sanitized error details; every column has a writer in internal/repository/ops_repo.go (enforced by scripts/checks/ops-error-log-column-writers.py).';
