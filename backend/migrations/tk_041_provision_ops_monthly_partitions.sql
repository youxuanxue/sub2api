-- TK perf/safety: proactively provision the upcoming monthly partitions for the
-- partitioned ops tables.
--
-- Context: ops_system_logs / ops_error_logs were converted to RANGE-partitioned
-- tables whose only child today is the wide legacy partition
-- `FOR VALUES FROM (MINVALUE) TO ('2026-07-01')`. That partition absorbs all
-- current writes, but there is NO partition for 2026-07-01 onward — so once the
-- calendar crosses 2026-07-01, an insert with created_at >= 2026-07-01 would have
-- no home and FAIL (there is no DEFAULT partition). The daily cleanup's
-- EnsureMonthly is supposed to keep future months provisioned, but on prod none
-- existed as of this change, so this migration guarantees the routing safety net
-- independent of the background job.
--
-- Names + bounds match pgpartition.EnsureMonthly exactly (table_YYYYMM, monthly
-- [first-of-month, first-of-next-month)), so this is idempotent with the job:
-- whichever runs first wins, the other is a no-op via IF NOT EXISTS. No overlap
-- with the legacy partition (legacy is exclusive at 2026-07-01). Empty partitions
-- cost nothing.
--
-- This also unblocks retention going forward: once writes land in dated monthly
-- partitions, DropExpired can drop whole past months instantly (and tk_040's
-- straddling chunked-DELETE bounds the legacy partition until it ages out).

CREATE TABLE IF NOT EXISTS ops_system_logs_202607 PARTITION OF ops_system_logs FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE IF NOT EXISTS ops_system_logs_202608 PARTITION OF ops_system_logs FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE IF NOT EXISTS ops_system_logs_202609 PARTITION OF ops_system_logs FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE IF NOT EXISTS ops_system_logs_202610 PARTITION OF ops_system_logs FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

CREATE TABLE IF NOT EXISTS ops_error_logs_202607 PARTITION OF ops_error_logs FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE IF NOT EXISTS ops_error_logs_202608 PARTITION OF ops_error_logs FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE IF NOT EXISTS ops_error_logs_202609 PARTITION OF ops_error_logs FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE IF NOT EXISTS ops_error_logs_202610 PARTITION OF ops_error_logs FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
