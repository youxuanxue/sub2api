-- Apply only to the separate QA queue database, as its bootstrap administrator.
-- No passwords, login roles, or application-database migrations belong here.
\set ON_ERROR_STOP on
BEGIN;
DO $$ BEGIN
  IF current_database() <> 'tokenkey_qa_queue' THEN
    RAISE EXCEPTION 'requires the dedicated tokenkey_qa_queue database';
  END IF;
END $$;
CREATE ROLE qa_queue_owner NOLOGIN;
CREATE ROLE qa_queue_producer NOLOGIN;
CREATE ROLE qa_queue_consumer NOLOGIN;
REVOKE ALL ON DATABASE tokenkey_qa_queue FROM PUBLIC;
GRANT CONNECT ON DATABASE tokenkey_qa_queue TO qa_queue_producer, qa_queue_consumer;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA qa_queue AUTHORIZATION qa_queue_owner;
SET LOCAL ROLE qa_queue_owner;
CREATE TABLE qa_queue.jobs (
  spec_key text PRIMARY KEY CHECK (spec_key ~ '^qa-bundles/v1/jobs/[0-9a-f]{64}/spec[.]json$'),
  state text NOT NULL DEFAULT 'ready' CHECK (state IN ('ready', 'leased', 'done', 'dead')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
  receipt uuid,
  lease_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK ((state = 'leased' AND receipt IS NOT NULL AND lease_until IS NOT NULL)
      OR (state <> 'leased' AND receipt IS NULL AND lease_until IS NULL))
);
CREATE INDEX jobs_pending ON qa_queue.jobs (created_at, spec_key) WHERE state IN ('ready', 'leased');
CREATE TABLE qa_queue.control (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), paused boolean NOT NULL DEFAULT false);
INSERT INTO qa_queue.control DEFAULT VALUES;

CREATE FUNCTION qa_queue.version() RETURNS integer LANGUAGE sql
  SET search_path = pg_catalog, pg_temp AS $$ SELECT 1 $$;

CREATE FUNCTION qa_queue.enqueue(key text) RETURNS void LANGUAGE sql SECURITY DEFINER
  SET search_path = pg_catalog, pg_temp AS $$
  INSERT INTO qa_queue.jobs(spec_key) VALUES (key) ON CONFLICT DO NOTHING;
$$;

-- One active lease across all consumers, including accidental duplicate workers.
-- An expired worker is fenced at ACK; execution must finish before its 1h lease.
CREATE FUNCTION qa_queue.claim() RETURNS TABLE(spec_key text, receipt uuid, attempts integer)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
  PERFORM 1 FROM qa_queue.control WHERE singleton FOR UPDATE;
  IF (SELECT paused FROM qa_queue.control WHERE singleton) THEN RETURN; END IF;
  IF EXISTS (SELECT 1 FROM qa_queue.jobs j WHERE state = 'leased' AND lease_until > clock_timestamp()) THEN RETURN; END IF;
  UPDATE qa_queue.jobs SET state = 'dead', receipt = NULL, lease_until = NULL, updated_at = clock_timestamp()
    WHERE state = 'leased' AND lease_until <= clock_timestamp() AND qa_queue.jobs.attempts = 3;
  RETURN QUERY
    WITH candidate AS (
      SELECT j.spec_key FROM qa_queue.jobs j
      WHERE state = 'ready' OR (state = 'leased' AND lease_until <= clock_timestamp())
      ORDER BY created_at, j.spec_key FOR UPDATE SKIP LOCKED LIMIT 1
    )
    UPDATE qa_queue.jobs j SET state = 'leased', attempts = j.attempts + 1,
      receipt = gen_random_uuid(), lease_until = clock_timestamp() + interval '1 hour', updated_at = clock_timestamp()
    FROM candidate c WHERE j.spec_key = c.spec_key
    RETURNING j.spec_key, j.receipt, j.attempts;
END $$;

CREATE FUNCTION qa_queue.ack(key text, handle uuid) RETURNS boolean
  LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
  WITH acknowledged AS (
    UPDATE qa_queue.jobs SET state = 'done', receipt = NULL, lease_until = NULL, updated_at = clock_timestamp()
    WHERE spec_key = key AND state = 'leased' AND receipt = handle AND lease_until > clock_timestamp()
    RETURNING 1
  ) SELECT EXISTS (SELECT 1 FROM acknowledged);
$$;

REVOKE ALL ON ALL TABLES IN SCHEMA qa_queue FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA qa_queue FROM PUBLIC;
GRANT USAGE ON SCHEMA qa_queue TO qa_queue_producer, qa_queue_consumer;
GRANT EXECUTE ON FUNCTION qa_queue.version() TO qa_queue_producer, qa_queue_consumer;
GRANT EXECUTE ON FUNCTION qa_queue.enqueue(text) TO qa_queue_producer;
GRANT EXECUTE ON FUNCTION qa_queue.claim(), qa_queue.ack(text, uuid) TO qa_queue_consumer;
COMMIT;
