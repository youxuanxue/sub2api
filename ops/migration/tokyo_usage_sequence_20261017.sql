-- One-time repair of the omitted partition-parent sequence. Never lower it.
-- ALTER serializes with nextval/setval without locking usage tables.
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '250ms';
SET LOCAL statement_timeout = '2s';
DO $$ BEGIN
  IF (SELECT seqincrement <> 1 OR seqcache <> 1 OR seqcycle
      FROM pg_sequence WHERE seqrelid='public.usage_logs_id_seq'::regclass) THEN
    RAISE EXCEPTION 'unexpected usage sequence definition';
  END IF;
END $$;
ALTER SEQUENCE public.usage_logs_id_seq CACHE 1;
DO $$ BEGIN
  IF (SELECT last_value FROM public.usage_logs_id_seq) < 14727151 THEN
    PERFORM setval('public.usage_logs_id_seq', 14727151, true);
  END IF;
END $$;
COMMIT;
