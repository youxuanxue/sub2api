-- TK perf/safety: repeat the ops monthly partition safety net with overlap-safe
-- handling, without mutating already-applied tk_041.
--
-- Some nodes applied tk_035/tk_037 later, so their legacy partition upper bound
-- can already cover the static tk_041 month targets. In that shape, a direct
-- CREATE PARTITION raises SQLSTATE 42P17 (overlap). Treat that as the same
-- benign no-op as the runtime pgpartition.EnsureMonthly helper: if the month is
-- already covered, writes have a home.

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
        NULL;
    END;
  END LOOP;
END $$;
