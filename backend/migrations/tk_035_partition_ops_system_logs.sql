-- tk_035_partition_ops_system_logs.sql
-- Convert ops_system_logs (plain table, BIGSERIAL id, created_at NOT NULL) to
-- monthly RANGE partitioning by created_at, so retention becomes instant DROP
-- PARTITION instead of the chunked ctid/id DELETE (bloat + autovacuum debt). This
-- is WAVE 1 of the data-layer partition program (the cleanest topology: no FK in/out,
-- no non-time unique index, no ON CONFLICT(id), append-only via COPY).
--
-- NO PRIMARY KEY on the partitioned parent: ops_system_logs has nothing referencing
-- its id, no upsert on id, and every read uses the (created_at, id) composite index —
-- so id stays a sequence-backed auto-increment column WITHOUT a uniqueness constraint.
-- (A partitioned PK would be forced to include created_at and rebuild a multi-GB index;
-- there is no functional need for it here.)
--
-- Technique (attach-legacy, no data copy): rename the live table to *_legacy, build a
-- partitioned parent inheriting the id sequence default, recreate the secondary indexes
-- on the parent, ATTACH the legacy table as the historical partition [MINVALUE, this
-- month), then create going-forward monthly partitions. Postgres attaches the legacy's
-- existing matching indexes in place (no rebuild). Idempotent + transactional (atomic):
-- re-runs are no-ops once partitioned.
--
-- Runs at startup with no concurrent writes (app not yet serving). The going-forward
-- monthly partitions are then maintained by the runtime partition-retention mechanism
-- (see backend/internal/repository/partition_retention*.go), and retention DROPs old
-- monthly partitions instead of DELETE.

DO $$
DECLARE
  -- Legacy holds ALL existing rows (history + the current partial month), so its upper
  -- bound is next_month. Going-forward monthly partitions begin at next_month; new
  -- inserts during the current month route into the legacy partition until then.
  next_month  date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month')::date;
  after_next  date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '2 month')::date;
  after_2     date := (date_trunc('month', now() AT TIME ZONE 'UTC') + interval '3 month')::date;
  idx record;
BEGIN
  -- Idempotent: already partitioned -> nothing to do.
  IF EXISTS (
    SELECT 1 FROM pg_partitioned_table pt
    JOIN pg_class c ON c.oid = pt.partrelid
    WHERE c.relname = 'ops_system_logs'
  ) THEN
    RETURN;
  END IF;

  -- Only convert an existing plain table. (Fresh DBs may already create it partitioned
  -- via a future path; guard against acting on a missing table.)
  IF NOT EXISTS (
    SELECT 1 FROM pg_class WHERE relname = 'ops_system_logs' AND relkind = 'r'
  ) THEN
    RETURN;
  END IF;

  -- 1. Rename live table to legacy; drop its PK(id) (not needed) and free the canonical
  --    index names so the partitioned parent can own them.
  ALTER TABLE ops_system_logs RENAME TO ops_system_logs_legacy;
  ALTER TABLE ops_system_logs_legacy DROP CONSTRAINT IF EXISTS ops_system_logs_pkey;
  FOR idx IN
    SELECT indexname FROM pg_indexes
    WHERE schemaname = 'public' AND tablename = 'ops_system_logs_legacy'
      AND indexname LIKE 'idx_ops_system_logs_%'
      AND indexname NOT LIKE '%\_legacy'
  LOOP
    EXECUTE format('ALTER INDEX %I RENAME TO %I', idx.indexname, idx.indexname || '_legacy');
  END LOOP;

  -- 2. Partitioned parent, inheriting the id sequence DEFAULT (so COPY inserts keep
  --    auto-assigning ids from the shared sequence) and storage params.
  CREATE TABLE ops_system_logs (
    LIKE ops_system_logs_legacy INCLUDING DEFAULTS INCLUDING STORAGE
  ) PARTITION BY RANGE (created_at);

  -- 3. Canonical secondary indexes on the parent (partitioned indexes). When the legacy
  --    partition is attached below, Postgres matches its existing *_legacy indexes by
  --    definition and attaches them in place (no rebuild).
  CREATE INDEX idx_ops_system_logs_created_at_id ON ops_system_logs (created_at DESC, id DESC);
  CREATE INDEX idx_ops_system_logs_level_created_at ON ops_system_logs (level, created_at DESC);
  CREATE INDEX idx_ops_system_logs_component_created_at ON ops_system_logs (component, created_at DESC);
  CREATE INDEX idx_ops_system_logs_request_id ON ops_system_logs (request_id);
  CREATE INDEX idx_ops_system_logs_client_request_id ON ops_system_logs (client_request_id);
  CREATE INDEX idx_ops_system_logs_user_id_created_at ON ops_system_logs (user_id, created_at DESC);
  CREATE INDEX idx_ops_system_logs_account_id_created_at ON ops_system_logs (account_id, created_at DESC);
  CREATE INDEX idx_ops_system_logs_platform_model_created_at ON ops_system_logs (platform, model, created_at DESC);
  CREATE INDEX idx_ops_system_logs_message_search ON ops_system_logs USING GIN (to_tsvector('simple', COALESCE(message, '')));

  -- 4. Attach legacy as the historical partition [MINVALUE, next_month): it holds ALL
  --    existing rows including the current partial month. ATTACH scans legacy once to
  --    prove every row is < next_month (always true for existing data); at startup with
  --    no traffic this is a one-time cost. Retention later drops this partition (by its
  --    upper bound) once next_month is past the cutoff — see partition_retention*.go.
  EXECUTE format(
    'ALTER TABLE ops_system_logs ATTACH PARTITION ops_system_logs_legacy FOR VALUES FROM (MINVALUE) TO (%L)',
    next_month
  );

  -- 5. Going-forward monthly partitions (next month + the one after), so live inserts
  --    have a home when the calendar rolls over; the runtime mechanism creates further
  --    months on its tick. The current partial month stays in the legacy partition.
  EXECUTE format(
    'CREATE TABLE IF NOT EXISTS ops_system_logs_%s PARTITION OF ops_system_logs FOR VALUES FROM (%L) TO (%L)',
    to_char(next_month, 'YYYYMM'), next_month, after_next
  );
  EXECUTE format(
    'CREATE TABLE IF NOT EXISTS ops_system_logs_%s PARTITION OF ops_system_logs FOR VALUES FROM (%L) TO (%L)',
    to_char(after_next, 'YYYYMM'), after_next, after_2
  );
END $$;
