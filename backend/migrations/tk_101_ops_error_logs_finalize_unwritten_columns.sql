-- high-risk-anchor: docs/approved/ops-error-logs-column-contract.md
-- 阶段 2:物理删除 ops_error_logs 上九列没有写入方的列,并删掉明文 key 审计表。
--
-- 这条迁移是 tk_100 + 上游 finalize-ingress-reject-cleanup.sql 的合并落地。
-- tk_100 当初被登记为「只记录不执行」(migrations_runner.go 的
-- RetainedOpsErrorColumnsMigration 分支),因为它声明的 DROP 会在
-- 1.8.261 的 blue/green 窗口里砍掉上一版本 1.8.260 仍在 SQL 里点名的列。
-- 迁移文件不可变,checksum 已入库,所以 tk_100 永远不会再执行 —— 它那六列
-- 必须由本迁移接手。
--
-- 两组列合并在一条迁移里,因为它们的前置条件和风险面完全相同,分两条只会让
-- 92 个分区的 ACCESS EXCLUSIVE 锁获取做两遍。
--
-- 第一组(tk_100 声明但从未执行,由 033_ops_monitoring_vnext.sql 引入):
--   duration_ms / network_error_type / provider_error_code /
--   provider_error_type / account_status / retry_after_seconds
-- 第二组(145_deleted_api_key_audit.sql 引入的已删除 key 归因):
--   attempted_key_prefix / deleted_key_owner_user_id / deleted_key_name
--
-- 执行前实测(prod,2026-09-27):2,213,022 行里这九列的非空计数**全部为 0**,
-- 无索引、无视图、无外键依赖。零数据损失。
--
-- 读写方已全部摘除:归因写入方在 c219055ba(#2353)删除,读取方同批改为直接
-- user_id 归因;tk_100 六列从未有写入方,读取侧在 c05dedf35(#2349)改到有
-- 写入方的等价列(duration_ms -> response_latency_ms,network_error_type ->
-- error_type/error_owner,provider_error_code -> upstream_status_code +
-- upstream_error_message,provider_error_type -> error_type,account_status ->
-- 关联 accounts.status,retry_after_seconds 是 136 移除重放存储后的残留)。
--
-- bluegreen-safe-destructive-ok: 窗口内新旧 color 都跑 1.8.261 或更高,其
-- 生产代码对这九列既不写也不读(已 git grep 核实 v1.8.261 全树,命中只剩注释、
-- 迁移历史文件和本 finalizer 自身;ops 探针里的 account_status 是
-- accounts.status 的别名,不是本表列)。代价是明确接受的:执行后回滚到
-- 1.8.260 会 42703,因为 1.8.260 的 ops_repo_user_visible_failure_tk.go 和
-- ops_repo_routing_capacity_tk.go 仍在 SQL 里点名归因三列。该取舍由运维在
-- 2026-09-27 全 fleet(prod + us3/us4/us5/us6)升到 1.8.261 且两阶段
-- post-check 双 green 之后批准。
--
-- 每个 Edge 各有独立 Postgres,所以本迁移会在 5 个库上分别执行,由各自的
-- 应用启动时的迁移 runner 驱动 —— 不需要手工逐库操作。
--
-- lock_timeout 沿用上游 finalizer 的 5s:DROP COLUMN 只改 catalog,不重写
-- 数据,但要在 92 个分区加 ACCESS EXCLUSIVE。5s 内拿不到锁就整条失败回滚,
-- 宁可下次部署重试,也不在高峰期把写入堵死。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE IF EXISTS ops_error_logs
    DROP COLUMN IF EXISTS duration_ms,
    DROP COLUMN IF EXISTS network_error_type,
    DROP COLUMN IF EXISTS provider_error_code,
    DROP COLUMN IF EXISTS provider_error_type,
    DROP COLUMN IF EXISTS account_status,
    DROP COLUMN IF EXISTS retry_after_seconds,
    DROP COLUMN IF EXISTS attempted_key_prefix,
    DROP COLUMN IF EXISTS deleted_key_owner_user_id,
    DROP COLUMN IF EXISTS deleted_key_name;

-- 明文 key 审计表:唯一的读取方(已删除 key 归因)已随 #2353 删除,写入方在
-- 同一个 PR 里从 api_key_repo.DeleteWithAudit 摘掉。留着等于为一个没有读取方
-- 的表长期保存提交上来的 key 前缀。prod 实测 136 行,且该表从无清理策略(无界),
-- 而 ops_error_logs 本身 30 天到期 —— 留着它只会让明文比错误日志活得更久。
DROP TABLE IF EXISTS deleted_api_key_audits;

-- tk_100 末尾这条 COMMENT 从未执行过(整条迁移只记录不执行),表注释至今还停在
-- 033 的版本。由本迁移补上,让「每列都有写入方」这个契约在库里也能自证。
COMMENT ON TABLE ops_error_logs IS 'Ops error logs (vNext). Sanitized error details; every column has a writer in internal/repository/ops_repo.go (enforced by scripts/checks/ops-error-log-column-writers.py).';

-- DROP COLUMN 只标记 catalog 不回收空间;VACUUM 放到正常维护窗口单独做:
--   VACUUM (ANALYZE) ops_error_logs;
