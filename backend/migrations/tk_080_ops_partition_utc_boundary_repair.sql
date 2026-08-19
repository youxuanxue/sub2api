-- tk_080_ops_partition_utc_boundary_repair.sql
-- Repair ops partition bounds created by tk_035/tk_037 in a non-UTC session.
--
-- This predecessor intentionally sorts after tk_080_cloudwise_* and before the
-- already-published tk_081_ops_daily_partition_cutover.sql. Nodes where the
-- current writer already ends at the next UTC month are strict no-ops.
--
-- The repair preserves every historical partition and the current writer OID.
-- Only exact, empty future monthly children with the same sub-day timezone
-- offset may be removed. Exact canonical UTC monthly children are preserved for
-- tk_081 to inspect and cut over.
-- bluegreen-safe-destructive-ok: DROP TABLE is limited to exact shifted future
-- children proven empty before and under lock; old instances still write through
-- the unchanged parent, and tk_081 immediately provisions canonical daily ranges.
-- A validated CHECK lets PostgreSQL adjust the
-- current writer bound without scanning it while holding the parent lock.

SET LOCAL TIME ZONE 'UTC';
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10min';

DO $migration$
<<repair>>
DECLARE
  schema_name text := current_schema();
  repair_now timestamptz := COALESCE(
    current_setting('tokenkey.ops_daily_cutover_now', true),
    transaction_timestamp()::text
  )::timestamptz;
  current_start timestamptz := date_trunc('month', repair_now);
  next_start timestamptz := date_trunc('month', repair_now) + interval '1 month';
  horizon_end timestamptz := date_trunc('month', repair_now) + interval '4 months';
  parent_name text;
  parent_oid oid;
  current_child_oid oid;
  current_child_name text;
  current_bound_expr text;
  current_lower_unbounded boolean;
  current_lower timestamptz;
  current_upper timestamptz;
  boundary_offset interval;
  month_start timestamptz;
  month_end timestamptz;
  expected_child_name text;
  future_rec record;
  state_rec record;
  future_child_oid oid;
  future_child_name text;
  future_bound_expr text;
  matching_count integer;
  unexpected_count integer;
  has_rows boolean;
  check_name text;
BEGIN
  CREATE TEMP TABLE tk_ops_utc_boundary_repair_state (
    parent_name text PRIMARY KEY,
    parent_oid oid NOT NULL,
    current_child_oid oid NOT NULL,
    current_child_name text NOT NULL,
    current_bound_expr text NOT NULL,
    current_lower_unbounded boolean NOT NULL,
    current_lower timestamptz,
    current_upper timestamptz NOT NULL,
    boundary_offset interval NOT NULL,
    check_name text NOT NULL
  ) ON COMMIT DROP;

  CREATE TEMP TABLE tk_ops_utc_boundary_repair_future (
    parent_name text NOT NULL,
    month_start timestamptz NOT NULL,
    child_oid oid NOT NULL,
    child_name text NOT NULL,
    bound_expr text NOT NULL,
    state text NOT NULL CHECK (state IN ('shifted', 'canonical')),
    PRIMARY KEY (parent_name, month_start),
    UNIQUE (parent_name, child_oid)
  ) ON COMMIT DROP;

  FOREACH parent_name IN ARRAY ARRAY['ops_error_logs', 'ops_system_logs'] LOOP
    SELECT c.oid
      INTO parent_oid
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
      JOIN pg_partitioned_table pt ON pt.partrelid = c.oid
     WHERE n.nspname = schema_name
       AND c.relname = parent_name
       AND pt.partstrat = 'r'
       AND pt.partnatts = 1
       AND pg_get_partkeydef(c.oid) = 'RANGE (created_at)';
    IF parent_oid IS NULL THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: %.% must be RANGE (created_at) partitioned',
        schema_name, parent_name;
    END IF;

    SELECT count(*), min(child_oid), min(child_name), min(bound_expr),
           bool_and(lower_unbounded), min(lower_bound), min(upper_bound)
      INTO matching_count, current_child_oid, current_child_name,
           current_bound_expr, current_lower_unbounded, current_lower, current_upper
      FROM (
        SELECT child.oid AS child_oid,
               child.relname AS child_name,
               pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
               pg_get_expr(child.relpartbound, child.oid, true) LIKE
                 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
               substring(pg_get_expr(child.relpartbound, child.oid, true)
                         FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
               substring(pg_get_expr(child.relpartbound, child.oid, true)
                         FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
      ) bounds
     WHERE (lower_unbounded OR lower_bound <= current_start)
       AND upper_bound > current_start;
    IF matching_count <> 1 OR current_child_oid IS NULL OR current_upper IS NULL THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: %.% must have exactly one child containing the current UTC instant range',
        schema_name, parent_name;
    END IF;

    -- Canonical current bounds, including nodes where tk_081 already completed,
    -- need no repair and must retain their existing topology verbatim.
    IF current_upper = next_start THEN
      CONTINUE;
    END IF;

    boundary_offset := current_upper - next_start;
    IF boundary_offset = interval '0'
       OR abs(extract(epoch FROM boundary_offset)) >= 86400 THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: %.% current boundary offset % is not a sub-day timezone skew',
        schema_name, parent_name, boundary_offset;
    END IF;

    check_name := left(current_child_name, 38) || '_tk_utc_bound_check';
    INSERT INTO tk_ops_utc_boundary_repair_state VALUES (
      parent_name, parent_oid, current_child_oid, current_child_name,
      current_bound_expr, current_lower_unbounded, current_lower, current_upper,
      boundary_offset, check_name
    );

    -- Every child after the current writer must be absent, an exact shifted
    -- monthly child from the historical session, or an exact canonical UTC
    -- monthly child created by the runtime owner. Only shifted children belong
    -- to this repair; canonical children remain owned by tk_081.
    month_start := next_start;
    WHILE month_start < horizon_end LOOP
      month_end := month_start + interval '1 month';
      expected_child_name := parent_name || '_' || to_char(month_start, 'YYYYMM');

      SELECT count(*), min(child_oid), min(child_name), min(bound_expr)
        INTO matching_count, future_child_oid, future_child_name, future_bound_expr
        FROM (
          SELECT child.oid AS child_oid,
                 child.relname AS child_name,
                 pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
                 substring(pg_get_expr(child.relpartbound, child.oid, true)
                           FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
                 substring(pg_get_expr(child.relpartbound, child.oid, true)
                           FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
            FROM pg_inherits inheritance
            JOIN pg_class child ON child.oid = inheritance.inhrelid
           WHERE inheritance.inhparent = parent_oid
             AND child.oid <> current_child_oid
        ) bounds
       WHERE child_name = expected_child_name
         AND (
           (lower_bound = month_start + boundary_offset
             AND upper_bound = month_end + boundary_offset)
           OR
           (lower_bound = month_start AND upper_bound = month_end)
         );
      IF matching_count > 1 THEN
        RAISE EXCEPTION 'tk_080 ops UTC repair: %.% has duplicate future child for month %',
          schema_name, parent_name, month_start;
      END IF;
      IF matching_count = 1 THEN
        IF substring(future_bound_expr FROM $$FROM \('([^']+)'$$)::timestamptz =
             month_start + boundary_offset THEN
          EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I.%I LIMIT 1)',
                         schema_name, future_child_name)
            INTO has_rows;
          IF has_rows THEN
            RAISE EXCEPTION 'tk_080 ops UTC repair: refusing to replace non-empty future child %.%',
              schema_name, future_child_name;
          END IF;
          INSERT INTO tk_ops_utc_boundary_repair_future VALUES (
            parent_name, month_start, future_child_oid, future_child_name,
            future_bound_expr, 'shifted'
          );
        ELSE
          INSERT INTO tk_ops_utc_boundary_repair_future VALUES (
            parent_name, month_start, future_child_oid, future_child_name,
            future_bound_expr, 'canonical'
          );
        END IF;
      END IF;
      month_start := month_end;
    END LOOP;

    SELECT count(*)
      INTO unexpected_count
      FROM (
        SELECT child.oid AS child_oid,
               substring(pg_get_expr(child.relpartbound, child.oid, true)
                         FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
           AND child.oid <> current_child_oid
      ) children
     WHERE (upper_bound IS NULL OR upper_bound > current_start)
       AND NOT EXISTS (
         SELECT 1
           FROM tk_ops_utc_boundary_repair_future future
          WHERE future.parent_name = repair.parent_name
            AND future.child_oid = children.child_oid
       );
    IF unexpected_count <> 0 THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: %.% has unexpected current/future partition topology',
        schema_name, parent_name;
    END IF;

    -- Validate outside the short parent-lock phase. RowExclusive writers remain
    -- compatible with CHECK validation, and the exact constraint lets ATTACH
    -- reuse proof instead of scanning the current writer under AccessExclusive.
    IF current_lower_unbounded THEN
      EXECUTE format(
        'ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK (created_at < %L::timestamptz) NOT VALID',
        schema_name, current_child_name, check_name, next_start
      );
    ELSE
      EXECUTE format(
        'ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK (created_at >= %L::timestamptz AND created_at < %L::timestamptz) NOT VALID',
        schema_name, current_child_name, check_name, current_lower, next_start
      );
    END IF;
    EXECUTE format('ALTER TABLE %I.%I VALIDATE CONSTRAINT %I',
                   schema_name, current_child_name, check_name);
  END LOOP;

  -- No affected parent means the complete migration is a strict no-op.
  IF NOT EXISTS (SELECT 1 FROM tk_ops_utc_boundary_repair_state) THEN
    RETURN;
  END IF;

  -- The destructive phase is metadata-only and fail-closed under the short lock.
  FOR state_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_state ORDER BY parent_name
  LOOP
    EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE',
                   schema_name, state_rec.parent_name);
  END LOOP;
  FOR future_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_future
     WHERE state = 'shifted'
     ORDER BY parent_name, child_name
  LOOP
    EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE',
                   schema_name, future_rec.child_name);
  END LOOP;

  -- Re-check all catalog and emptiness assumptions while the parent/children are locked.
  FOR state_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_state ORDER BY parent_name
  LOOP
    SELECT count(*)
      INTO matching_count
      FROM pg_inherits inheritance
      JOIN pg_class child ON child.oid = inheritance.inhrelid
     WHERE inheritance.inhparent = state_rec.parent_oid
       AND child.oid = state_rec.current_child_oid
       AND child.relname = state_rec.current_child_name
       AND pg_get_expr(child.relpartbound, child.oid, true) = state_rec.current_bound_expr;
    IF matching_count <> 1 THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: current writer changed for %.%',
        schema_name, state_rec.parent_name;
    END IF;
  END LOOP;

  FOR future_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_future ORDER BY parent_name, child_name
  LOOP
    SELECT count(*)
      INTO matching_count
      FROM pg_inherits inheritance
      JOIN pg_class child ON child.oid = inheritance.inhrelid
     WHERE inheritance.inhparent = to_regclass(
             format('%I.%I', schema_name, future_rec.parent_name)
           )
       AND child.oid = future_rec.child_oid
       AND child.relname = future_rec.child_name
       AND pg_get_expr(child.relpartbound, child.oid, true) = future_rec.bound_expr;
    IF matching_count <> 1 THEN
      RAISE EXCEPTION 'tk_080 ops UTC repair: future child changed for %.%',
        schema_name, future_rec.child_name;
    END IF;
    IF future_rec.state = 'shifted' THEN
      EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I.%I LIMIT 1)',
                     schema_name, future_rec.child_name)
        INTO has_rows;
      IF has_rows THEN
        RAISE EXCEPTION 'tk_080 ops UTC repair: refusing to replace non-empty future child %.%',
          schema_name, future_rec.child_name;
      END IF;
    END IF;
  END LOOP;

  FOR future_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_future
     WHERE state = 'shifted'
     ORDER BY parent_name, child_name
  LOOP
    EXECUTE format('DROP TABLE %I.%I', schema_name, future_rec.child_name);
  END LOOP;

  FOR state_rec IN
    SELECT * FROM tk_ops_utc_boundary_repair_state ORDER BY parent_name
  LOOP
    EXECUTE format('ALTER TABLE %I.%I DETACH PARTITION %I.%I',
                   schema_name, state_rec.parent_name,
                   schema_name, state_rec.current_child_name);
    IF state_rec.current_lower_unbounded THEN
      EXECUTE format(
        'ALTER TABLE %I.%I ATTACH PARTITION %I.%I FOR VALUES FROM (MINVALUE) TO (%L)',
        schema_name, state_rec.parent_name,
        schema_name, state_rec.current_child_name,
        next_start
      );
    ELSE
      EXECUTE format(
        'ALTER TABLE %I.%I ATTACH PARTITION %I.%I FOR VALUES FROM (%L) TO (%L)',
        schema_name, state_rec.parent_name,
        schema_name, state_rec.current_child_name,
        state_rec.current_lower, next_start
      );
    END IF;
    EXECUTE format('ALTER TABLE %I.%I DROP CONSTRAINT %I',
                   schema_name, state_rec.current_child_name, state_rec.check_name);
  END LOOP;
END $migration$;
