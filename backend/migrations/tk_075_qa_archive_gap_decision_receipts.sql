-- TK: immutable approval receipts for hash-bound QA archive gap decisions.
-- Window outcome remains owned exclusively by qa_archive_shards.
CREATE TABLE IF NOT EXISTS qa_archive_gap_decision_receipts (
    plan_hash text PRIMARY KEY CHECK (plan_hash ~ '^[0-9a-f]{64}$'),
    plan_schema_version text NOT NULL CHECK (plan_schema_version = 'qa-archive-gap-decision-v1'),
    plan_json jsonb NOT NULL,
    approved_by text NOT NULL CHECK (btrim(approved_by) <> ''),
    window_count integer NOT NULL CHECK (window_count > 0),
    applied_at timestamptz NOT NULL DEFAULT now(),
    CHECK (plan_json->>'plan_hash' = plan_hash),
    CHECK (plan_json->>'schema_version' = plan_schema_version),
    CHECK (jsonb_array_length(plan_json->'windows') = window_count)
);

CREATE OR REPLACE FUNCTION tk_qa_archive_gap_decision_receipts_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'qa_archive_gap_decision_receipts are append-only';
END;
$$;

DROP TRIGGER IF EXISTS trg_qa_archive_gap_decision_receipts_immutable
    ON qa_archive_gap_decision_receipts;
CREATE TRIGGER trg_qa_archive_gap_decision_receipts_immutable
BEFORE UPDATE OR DELETE ON qa_archive_gap_decision_receipts
FOR EACH ROW EXECUTE FUNCTION tk_qa_archive_gap_decision_receipts_immutable();

DROP TRIGGER IF EXISTS trg_qa_archive_gap_decision_receipts_no_truncate
    ON qa_archive_gap_decision_receipts;
CREATE TRIGGER trg_qa_archive_gap_decision_receipts_no_truncate
BEFORE TRUNCATE ON qa_archive_gap_decision_receipts
FOR EACH STATEMENT EXECUTE FUNCTION tk_qa_archive_gap_decision_receipts_immutable();
