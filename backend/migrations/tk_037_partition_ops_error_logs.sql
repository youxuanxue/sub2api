-- tk_037_partition_ops_error_logs.sql
-- WAVE 2 of the data-layer partition program. Converts ops_error_logs (1.4GB) to monthly
-- RANGE partitioning so retention becomes instant DROP PARTITION instead of chunked DELETE.
-- Same attach-legacy technique as tk_035 (ops_system_logs), with two differences:
--   (1) ops_error_logs has ~18 indexes (incl trigram + partial), so instead of hardcoding
--       them we CAPTURE pg_get_indexdef() before the rename and REPLAY them on the parent —
--       robust to every index without transcription risk;
--   (2) UpdateErrorResolution does `UPDATE ops_error_logs SET resolved... WHERE id = $1`,
--       which the dropped PK(id) used to serve, so we add a plain index on id. The resolution
--       UPDATE only touches non-key columns, so RANGE(created_at) partitioning is valid (no
--       cross-partition row movement).
--
-- NO PRIMARY KEY on the partitioned parent (no FK references it, no ON CONFLICT(id), no
-- non-time unique index) — id stays a sequence-backed auto-increment column. The id
-- sequence ownership is reassigned to the new parent so retention can DROP the legacy
-- partition cleanly (cf tk_035 review finding R-001). Idempotent, transactional, runs at
-- startup with no concurrent writes.

DO $$
DECLARE
  next_month date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month')::date;
  after_next date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '2 month')::date;
  after_2    date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '3 month')::date;
  idx_defs   text[];
  idx_names  text[];
  d text;
  n text;
BEGIN
  -- Idempotent: already partitioned -> nothing to do.
  IF EXISTS (
    SELECT 1 FROM pg_partitioned_table pt
    JOIN pg_class c ON c.oid = pt.partrelid
    WHERE c.relname = 'ops_error_logs'
  ) THEN
    RETURN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'ops_error_logs' AND relkind = 'r') THEN
    RETURN;
  END IF;

  -- 1. Capture every non-PK index definition (they reference 'ops_error_logs', which becomes
  --    the new parent) and the index names to rename on the legacy table. Done BEFORE any
  --    rename so the captured CREATE INDEX statements target the soon-to-exist parent.
  SELECT array_agg(pg_get_indexdef(x.indexrelid) ORDER BY i.relname),
         array_agg(i.relname ORDER BY i.relname)
  INTO idx_defs, idx_names
  FROM pg_index x
  JOIN pg_class i ON i.oid = x.indexrelid
  JOIN pg_class t ON t.oid = x.indrelid
  WHERE t.relname = 'ops_error_logs' AND NOT x.indisprimary;

  -- 2. Rename live table -> legacy, drop its PK(id), free the canonical index names.
  ALTER TABLE ops_error_logs RENAME TO ops_error_logs_legacy;
  ALTER TABLE ops_error_logs_legacy DROP CONSTRAINT IF EXISTS ops_error_logs_pkey;
  IF idx_names IS NOT NULL THEN
    FOREACH n IN ARRAY idx_names LOOP
      EXECUTE format('ALTER INDEX %I RENAME TO %I', n, n || '_legacy');
    END LOOP;
  END IF;

  -- 3. Partitioned parent (no PK), inheriting the id sequence DEFAULT + storage. Reassign
  --    the sequence ownership from the legacy partition to the parent (tk_035 R-001).
  CREATE TABLE ops_error_logs (
    LIKE ops_error_logs_legacy INCLUDING DEFAULTS INCLUDING STORAGE
  ) PARTITION BY RANGE (created_at);
  ALTER SEQUENCE IF EXISTS ops_error_logs_id_seq OWNED BY ops_error_logs.id;

  -- 4. Re-add the id index (UpdateErrorResolution's WHERE id = $1 was served by the PK),
  --    then replay all captured index definitions on the parent (canonical names). When the
  --    legacy partition is attached, its renamed *_legacy indexes match these by definition
  --    and attach in place (no rebuild); only idx_ops_error_logs_id is built on legacy (it
  --    lacked one after the PK drop) — cheap at this table's size.
  CREATE INDEX idx_ops_error_logs_id ON ops_error_logs (id);
  IF idx_defs IS NOT NULL THEN
    FOREACH d IN ARRAY idx_defs LOOP
      EXECUTE d;
    END LOOP;
  END IF;

  -- 5. Attach legacy as the historical partition [MINVALUE, next_month) (all existing rows
  --    incl the current partial month).
  EXECUTE format(
    'ALTER TABLE ops_error_logs ATTACH PARTITION ops_error_logs_legacy FOR VALUES FROM (MINVALUE) TO (%L)',
    next_month
  );

  -- 6. Going-forward monthly partitions.
  EXECUTE format(
    'CREATE TABLE IF NOT EXISTS ops_error_logs_%s PARTITION OF ops_error_logs FOR VALUES FROM (%L) TO (%L)',
    to_char(next_month, 'YYYYMM'), next_month, after_next
  );
  EXECUTE format(
    'CREATE TABLE IF NOT EXISTS ops_error_logs_%s PARTITION OF ops_error_logs FOR VALUES FROM (%L) TO (%L)',
    to_char(after_next, 'YYYYMM'), after_next, after_2
  );
END $$;
