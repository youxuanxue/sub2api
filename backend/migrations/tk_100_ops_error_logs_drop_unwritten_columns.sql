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
