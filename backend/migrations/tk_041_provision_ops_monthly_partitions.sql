-- TK perf/safety: proactively provision the upcoming monthly partitions for the
-- partitioned ops tables.
--
-- Context: ops_system_logs / ops_error_logs were converted to RANGE-partitioned
-- tables whose legacy partition upper bound is computed at conversion time
-- (tk_035/tk_037: [MINVALUE, next_month) where next_month rolls with UTC
-- calendar). Going-forward monthly partitions must exist so inserts after the
-- legacy bound have a home (no DEFAULT partition). The daily cleanup's
-- EnsureMonthly is supposed to keep future months provisioned, but on prod none
-- existed as of this change, so this migration guarantees the routing safety net
-- independent of the background job.
--
-- Static month targets below match the original 202607..202610 rollout. When the
-- legacy partition still covers a target month (calendar rolled past the original
-- tk_035 anchor), CREATE raises SQLSTATE 42P17 (overlap) — same benign case as
-- pgpartition.EnsureMonthly and is skipped here. IF NOT EXISTS + overlap-skip
-- keeps this idempotent with tk_035/tk_037 and the runtime job.

DO $$
DECLARE
  rec record;
BEGIN
  FOR rec IN
    SELECT * FROM (VALUES
      ('ops_system_logs', '202607', DATE '2026-07-01', DATE '2026-08-01'),
      ('ops_system_logs', '202608', DATE '2026-08-01', DATE '2026-09-01'),
      ('ops_system_logs', '202609', DATE '2026-09-01', DATE '2026-10-01'),
      ('ops_system_logs', '202610', DATE '2026-10-01', DATE '2026-11-01'),
      ('ops_error_logs', '202607', DATE '2026-07-01', DATE '2026-08-01'),
      ('ops_error_logs', '202608', DATE '2026-08-01', DATE '2026-09-01'),
      ('ops_error_logs', '202609', DATE '2026-09-01', DATE '2026-10-01'),
      ('ops_error_logs', '202610', DATE '2026-10-01', DATE '2026-11-01')
    ) AS v(parent_table, suffix, start_d, end_d)
  LOOP
    IF NOT EXISTS (
      SELECT 1
      FROM pg_partitioned_table pt
      JOIN pg_class c ON c.oid = pt.partrelid
      WHERE c.relname = rec.parent_table
    ) THEN
      CONTINUE;
    END IF;

    BEGIN
      EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF %I FOR VALUES FROM (%L) TO (%L)',
        rec.parent_table || '_' || rec.suffix,
        rec.parent_table,
        rec.start_d,
        rec.end_d
      );
    EXCEPTION
      WHEN duplicate_table THEN
        NULL;
      WHEN SQLSTATE '42P17' THEN
        NULL; -- month already covered by legacy or an existing sibling partition
    END;
  END LOOP;
END $$;
