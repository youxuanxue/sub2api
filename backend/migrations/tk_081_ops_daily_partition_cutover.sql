-- tk_081_ops_daily_partition_cutover.sql
-- Replace only empty future monthly ops partitions with complete UTC daily coverage.
-- The current-month writer partition and all historical partitions remain untouched.
--
-- bluegreen-safe-destructive-ok: DROP TABLE is limited to future monthly children
-- re-checked as empty under a 2s parent lock timeout. Complete daily month coverage
-- keeps the previous monthly owner compatible throughout the blue/green window.

SET LOCAL TIME ZONE 'UTC';
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '120s';

DO $migration$
DECLARE
  schema_name text := current_schema();
  cutover_now timestamptz := COALESCE(current_setting('tokenkey.ops_daily_cutover_now', true), transaction_timestamp()::text)::timestamptz;
  parent_name text;
  parent_oid oid;
  current_start timestamptz;
  next_start timestamptz;
  horizon_end timestamptz;
  month_start timestamptz;
  month_end timestamptz;
  day_start timestamptz;
  day_end timestamptz;
  child_name text;
  expected_name text;
  current_rec record;
  current_child_oid oid;
  current_bound_expr text;
  target_rec record;
  attached_rec record;
  relation_kind "char";
  has_rows boolean;
  invalid_count integer;
  matching_count integer;
  expected_days integer;
  attached_days integer;
BEGIN
  current_start := date_trunc('month', cutover_now);
  next_start := current_start + interval '1 month';
  horizon_end := current_start + interval '4 months';

  CREATE TEMP TABLE tk_ops_daily_current_state (
    parent_name text PRIMARY KEY,
    child_oid oid NOT NULL,
    bound_expr text NOT NULL
  ) ON COMMIT DROP;

  CREATE TEMP TABLE tk_ops_daily_target_state (
    parent_name text NOT NULL,
    month_start timestamptz NOT NULL,
    month_end timestamptz NOT NULL,
    state text NOT NULL CHECK (state IN ('absent', 'monthly', 'daily')),
    monthly_child_oid oid,
    monthly_child_name text,
    PRIMARY KEY (parent_name, month_start)
  ) ON COMMIT DROP;

  -- Validate the complete pre-cutover topology before creating detached tables.
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
      RAISE EXCEPTION 'tk_081: %.% must be RANGE (created_at) partitioned', schema_name, parent_name;
    END IF;

    SELECT count(*)
      INTO invalid_count
      FROM (
        SELECT pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
               pg_get_expr(child.relpartbound, child.oid, true) LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
      ) bounds
     WHERE bound_expr = 'DEFAULT'
        OR bound_expr LIKE '%MAXVALUE%'
        OR upper_bound IS NULL
        OR (NOT lower_unbounded AND lower_bound IS NULL);
    IF invalid_count <> 0 THEN
      RAISE EXCEPTION 'tk_081: %.% has DEFAULT, MAXVALUE, or unparseable child bounds', schema_name, parent_name;
    END IF;

    SELECT count(*), min(child_oid), min(bound_expr)
      INTO matching_count, current_child_oid, current_bound_expr
      FROM (
        SELECT child.oid AS child_oid,
               pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
               pg_get_expr(child.relpartbound, child.oid, true) LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
      ) bounds
     WHERE (lower_unbounded OR lower_bound <= current_start)
       AND upper_bound >= next_start;
    IF matching_count <> 1 OR current_child_oid IS NULL THEN
      RAISE EXCEPTION 'tk_081: %.% must have exactly one child covering the current UTC month', schema_name, parent_name;
    END IF;
    INSERT INTO tk_ops_daily_current_state VALUES (parent_name, current_child_oid, current_bound_expr);

    -- Anything beginning in the future must fit wholly inside the three-month owner horizon.
    SELECT count(*)
      INTO invalid_count
      FROM (
        SELECT pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
               substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
      ) bounds
     WHERE lower_bound >= next_start
       AND (upper_bound IS NULL OR lower_bound IS NULL OR upper_bound > horizon_end);
    IF invalid_count <> 0 THEN
      RAISE EXCEPTION 'tk_081: %.% has a future child outside the three-month horizon', schema_name, parent_name;
    END IF;

    month_start := next_start;
    WHILE month_start < horizon_end LOOP
      month_end := month_start + interval '1 month';
      expected_name := parent_name || '_' || to_char(month_start, 'YYYYMM');

      CREATE TEMP TABLE tk_ops_daily_month_children ON COMMIT DROP AS
      SELECT child.oid AS child_oid,
             child.relname AS child_name,
             pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr,
             substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
             substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
        FROM pg_inherits inheritance
        JOIN pg_class child ON child.oid = inheritance.inhrelid
       WHERE inheritance.inhparent = parent_oid
         AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz < month_end
         AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz > month_start;

      SELECT count(*) INTO matching_count FROM tk_ops_daily_month_children;
      IF matching_count = 0 THEN
        INSERT INTO tk_ops_daily_target_state VALUES (parent_name, month_start, month_end, 'absent', NULL, NULL);
      ELSIF matching_count = 1 AND EXISTS (
        SELECT 1 FROM tk_ops_daily_month_children month_child
         WHERE month_child.child_name = expected_name
           AND month_child.lower_bound = month_start
           AND month_child.upper_bound = month_end
      ) THEN
        SELECT month_child.child_oid, month_child.child_name INTO attached_rec
          FROM tk_ops_daily_month_children month_child;
        INSERT INTO tk_ops_daily_target_state
        VALUES (parent_name, month_start, month_end, 'monthly', attached_rec.child_oid, attached_rec.child_name);
      ELSE
        expected_days := (month_end::date - month_start::date);
        SELECT count(*) INTO attached_days
          FROM tk_ops_daily_month_children month_child
         WHERE month_child.child_name = parent_name || '_' || to_char(month_child.lower_bound, 'YYYYMMDD')
           AND month_child.lower_bound >= month_start
           AND month_child.upper_bound = month_child.lower_bound + interval '1 day'
           AND month_child.upper_bound <= month_end;
        IF matching_count = expected_days AND attached_days = expected_days THEN
          INSERT INTO tk_ops_daily_target_state VALUES (parent_name, month_start, month_end, 'daily', NULL, NULL);
        ELSE
          RAISE EXCEPTION 'tk_081: %.% has partial or unexpected coverage for month %', schema_name, parent_name, month_start;
        END IF;
      END IF;
      DROP TABLE tk_ops_daily_month_children;
      month_start := month_end;
    END LOOP;
  END LOOP;

  -- Build detached final-name tables and their local indexes before the short parent-lock phase.
  FOR target_rec IN
    SELECT * FROM tk_ops_daily_target_state
     WHERE state IN ('absent', 'monthly')
     ORDER BY parent_name, month_start
  LOOP
    day_start := target_rec.month_start;
    WHILE day_start < target_rec.month_end LOOP
      day_end := day_start + interval '1 day';
      child_name := target_rec.parent_name || '_' || to_char(day_start, 'YYYYMMDD');

      SELECT c.relkind INTO relation_kind
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
       WHERE n.nspname = schema_name AND c.relname = child_name;
      IF relation_kind IS NOT NULL THEN
        RAISE EXCEPTION 'tk_081: relation %.% already exists outside expected daily topology', schema_name, child_name;
      END IF;

      EXECUTE format('CREATE TABLE %I.%I (LIKE %I.%I INCLUDING ALL)',
                     schema_name, child_name, schema_name, target_rec.parent_name);
      EXECUTE format(
        'ALTER TABLE %I.%I ADD CONSTRAINT %I CHECK (created_at >= %L::timestamptz AND created_at < %L::timestamptz)',
        schema_name, child_name, child_name || '_utc_day_check', day_start, day_end
      );
      day_start := day_end;
    END LOOP;
  END LOOP;

  -- The catalog cutover is intentionally short: all table/index construction is complete.
  LOCK TABLE ops_error_logs IN ACCESS EXCLUSIVE MODE;
  LOCK TABLE ops_system_logs IN ACCESS EXCLUSIVE MODE;
  FOR target_rec IN
    SELECT * FROM tk_ops_daily_target_state
     WHERE state = 'monthly'
     ORDER BY parent_name, month_start
  LOOP
    EXECUTE format('LOCK TABLE %I.%I IN ACCESS EXCLUSIVE MODE', schema_name, target_rec.monthly_child_name);
  END LOOP;

  -- PostgreSQL DROP/ATTACH PARTITION requires ACCESS EXCLUSIVE on the parent.
  -- The short lock timeout makes an active writer a fail-closed retry condition.
  -- Re-check every load-bearing precondition under the parent and candidate-child locks.
  FOR current_rec IN SELECT * FROM tk_ops_daily_current_state ORDER BY parent_name LOOP
    SELECT count(*) INTO matching_count
      FROM pg_inherits inheritance
      JOIN pg_class child ON child.oid = inheritance.inhrelid
     WHERE inheritance.inhparent = to_regclass(format('%I.%I', schema_name, current_rec.parent_name))
       AND child.oid = current_rec.child_oid
       AND pg_get_expr(child.relpartbound, child.oid, true) = current_rec.bound_expr;
    IF matching_count <> 1 THEN
      RAISE EXCEPTION 'tk_081: current writer partition changed for %.%', schema_name, current_rec.parent_name;
    END IF;
  END LOOP;

  FOR target_rec IN SELECT * FROM tk_ops_daily_target_state ORDER BY parent_name, month_start LOOP
    parent_oid := to_regclass(format('%I.%I', schema_name, target_rec.parent_name));
    SELECT count(*) INTO matching_count
      FROM pg_inherits inheritance
      JOIN pg_class child ON child.oid = inheritance.inhrelid
     WHERE inheritance.inhparent = parent_oid
       AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz < target_rec.month_end
       AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz > target_rec.month_start;

    IF target_rec.state = 'monthly' THEN
      IF matching_count <> 1 OR NOT EXISTS (
        SELECT 1
          FROM pg_inherits inheritance
          JOIN pg_class child ON child.oid = inheritance.inhrelid
         WHERE inheritance.inhparent = parent_oid
           AND child.oid = target_rec.monthly_child_oid
           AND child.relname = target_rec.monthly_child_name
           AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz = target_rec.month_start
           AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz = target_rec.month_end
      ) THEN
        RAISE EXCEPTION 'tk_081: future monthly child changed for %.% month %', schema_name, target_rec.parent_name, target_rec.month_start;
      END IF;
      EXECUTE format('SELECT EXISTS (SELECT 1 FROM %I.%I LIMIT 1)', schema_name, target_rec.monthly_child_name)
        INTO has_rows;
      IF has_rows THEN
        RAISE EXCEPTION 'tk_081: refusing to replace non-empty future child %.%', schema_name, target_rec.monthly_child_name;
      END IF;
    ELSIF target_rec.state = 'absent' AND matching_count <> 0 THEN
      RAISE EXCEPTION 'tk_081: future topology appeared for %.% month %', schema_name, target_rec.parent_name, target_rec.month_start;
    ELSIF target_rec.state = 'daily' THEN
      expected_days := (target_rec.month_end::date - target_rec.month_start::date);
      IF matching_count <> expected_days THEN
        RAISE EXCEPTION 'tk_081: daily topology changed for %.% month %', schema_name, target_rec.parent_name, target_rec.month_start;
      END IF;
    END IF;
  END LOOP;

  -- Drop only locked, exactly-bounded, empty future monthly children.
  FOR target_rec IN
    SELECT * FROM tk_ops_daily_target_state
     WHERE state = 'monthly'
     ORDER BY parent_name, month_start
  LOOP
    EXECUTE format('DROP TABLE %I.%I', schema_name, target_rec.monthly_child_name);
  END LOOP;

  -- Attach every prebuilt day, preserving complete coverage for old and new owners.
  FOR target_rec IN
    SELECT * FROM tk_ops_daily_target_state
     WHERE state IN ('absent', 'monthly')
     ORDER BY parent_name, month_start
  LOOP
    day_start := target_rec.month_start;
    WHILE day_start < target_rec.month_end LOOP
      day_end := day_start + interval '1 day';
      child_name := target_rec.parent_name || '_' || to_char(day_start, 'YYYYMMDD');
      EXECUTE format(
        'ALTER TABLE %I.%I ATTACH PARTITION %I.%I FOR VALUES FROM (%L) TO (%L)',
        schema_name, target_rec.parent_name, schema_name, child_name, day_start, day_end
      );
      day_start := day_end;
    END LOOP;
  END LOOP;

  -- Final proof: every target month is made solely of exact, indexed UTC daily children.
  FOR target_rec IN SELECT * FROM tk_ops_daily_target_state ORDER BY parent_name, month_start LOOP
    parent_oid := to_regclass(format('%I.%I', schema_name, target_rec.parent_name));
    expected_days := (target_rec.month_end::date - target_rec.month_start::date);
    SELECT count(*) INTO attached_days
      FROM pg_inherits inheritance
      JOIN pg_class child ON child.oid = inheritance.inhrelid
     WHERE inheritance.inhparent = parent_oid
       AND child.relname = target_rec.parent_name || '_' || to_char(
             substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz,
             'YYYYMMDD'
           )
       AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz >= target_rec.month_start
       AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz <= target_rec.month_end
       AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz =
           substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz + interval '1 day';
    IF attached_days <> expected_days THEN
      RAISE EXCEPTION 'tk_081: incomplete daily coverage for %.% month %', schema_name, target_rec.parent_name, target_rec.month_start;
    END IF;

    -- PostgreSQL ATTACH automatically attaches or creates matching child indexes.
    IF EXISTS (
      SELECT 1
        FROM pg_index parent_index_meta
        JOIN pg_class parent_index ON parent_index.oid = parent_index_meta.indexrelid
       WHERE parent_index_meta.indrelid = parent_oid
         AND parent_index.relkind = 'I'
         AND EXISTS (
           SELECT 1
             FROM pg_inherits table_inheritance
             JOIN pg_class child ON child.oid = table_inheritance.inhrelid
            WHERE table_inheritance.inhparent = parent_oid
              AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$FROM \('([^']+)'$$)::timestamptz >= target_rec.month_start
              AND substring(pg_get_expr(child.relpartbound, child.oid, true) FROM $$TO \('([^']+)'$$)::timestamptz <= target_rec.month_end
              AND NOT EXISTS (
                SELECT 1
                  FROM pg_inherits index_inheritance
                  JOIN pg_index child_index_meta ON child_index_meta.indexrelid = index_inheritance.inhrelid
                 WHERE index_inheritance.inhparent = parent_index.oid
                   AND child_index_meta.indrelid = child.oid
              )
         )
    ) THEN
      RAISE EXCEPTION 'tk_081: child index attachment incomplete for %.% month %', schema_name, target_rec.parent_name, target_rec.month_start;
    END IF;
  END LOOP;
END $migration$;
