-- Only post-freeze rows in the collided ID interval are changed.
-- stdout is the private old/new ID receipt; caller must preserve it.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '250ms';
SET LOCAL statement_timeout = '5s';
SET LOCAL idle_in_transaction_session_timeout = '5s';
SET LOCAL max_parallel_workers_per_gather = 0;
SET LOCAL timezone = 'UTC';
CREATE TEMP TABLE repair_expected ON COMMIT DROP AS SELECT :'expected_content_md5'::text AS digest;
LOCK TABLE public.billing_usage_entries IN SHARE MODE NOWAIT;
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM public.billing_usage_entries) THEN
    RAISE EXCEPTION 'billing references need separate reconciliation';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE contype='f' AND confrelid='public.usage_logs'::regclass) THEN
    RAISE EXCEPTION 'usage foreign keys need separate reconciliation';
  END IF;
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public'
             AND column_name LIKE '%usage%id%'
             AND (table_name,column_name) <> ('billing_usage_entries','usage_log_id')) THEN
    RAISE EXCEPTION 'unreviewed logical usage reference';
  END IF;
  IF (SELECT last_value FROM public.usage_logs_id_seq) < 14727151 THEN
    RAISE EXCEPTION 'advance usage sequence first';
  END IF;
END $$;
CREATE TEMP TABLE repair_map ON COMMIT DROP AS
SELECT u.tableoid AS source_table, u.ctid AS source_row, u.id AS old_id,
       nextval('public.usage_logs_id_seq') AS new_id, u.created_at,
       to_jsonb(u) - 'id' AS body
FROM public.usage_logs u
WHERE u.created_at > '2026-10-17T02:54:43.473757Z'
  AND u.id BETWEEN 14724938 AND 14727151
FOR UPDATE;
DO $$ BEGIN
  IF (SELECT count(*) FROM repair_map) > 2214 THEN
    RAISE EXCEPTION 'repair exceeds approved cutover delta bound';
  END IF;
  IF (SELECT md5(coalesce(string_agg(md5((body || jsonb_build_object('id',old_id))::text), '' ORDER BY old_id,created_at),'')) FROM repair_map)
     IS DISTINCT FROM (SELECT digest FROM repair_expected) THEN
    RAISE EXCEPTION 'candidate content changed since private backup';
  END IF;
END $$;
CREATE TEMP TABLE repair_result ON COMMIT DROP AS
WITH changed AS (
  UPDATE public.usage_logs u SET id = m.new_id
  FROM repair_map m
  WHERE u.tableoid=m.source_table AND u.ctid=m.source_row
    AND u.created_at > '2026-10-17T02:54:43.473757Z'
    AND u.id BETWEEN 14724938 AND 14727151
  RETURNING u.*
) SELECT id, to_jsonb(changed) - 'id' AS body FROM changed;
DO $$ BEGIN
  IF (SELECT count(*) FROM repair_result) <> (SELECT count(*) FROM repair_map)
     OR EXISTS (SELECT 1 FROM repair_map m LEFT JOIN repair_result r ON r.id=m.new_id
                WHERE r.id IS NULL OR r.body IS DISTINCT FROM m.body) THEN
    RAISE EXCEPTION 'row count or non-ID content changed';
  END IF;
END $$;
\copy (SELECT old_id,new_id,created_at,md5(body::text) AS content_md5 FROM repair_map ORDER BY old_id) TO STDOUT WITH (FORMAT csv, HEADER true)
COMMIT;
