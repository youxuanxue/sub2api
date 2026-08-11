-- TK: immutable QA hourly storage cutover and append-only phase receipts.
CREATE TABLE IF NOT EXISTS qa_lifecycle_receipts (
    phase text PRIMARY KEY CHECK (phase IN ('activate', 'finalize')),
    plan_hash text NOT NULL CHECK (plan_hash ~ '^[0-9a-f]{64}$'),
    t0_utc timestamptz NOT NULL CHECK (
        t0_utc = date_bin(interval '1 hour', t0_utc, timestamptz '2000-01-01 00:00:00+00')
    ),
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION tk_qa_lifecycle_receipts_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'qa_lifecycle_receipts are append-only';
END;
$$;

DROP TRIGGER IF EXISTS trg_qa_lifecycle_receipts_immutable ON qa_lifecycle_receipts;
CREATE TRIGGER trg_qa_lifecycle_receipts_immutable
BEFORE UPDATE OR DELETE ON qa_lifecycle_receipts
FOR EACH ROW EXECUTE FUNCTION tk_qa_lifecycle_receipts_immutable();

CREATE OR REPLACE FUNCTION tk_qa_lifecycle_receipts_insert_guard()
RETURNS TRIGGER AS $$
BEGIN
  IF NEW.phase = 'finalize' AND NOT EXISTS (
    SELECT 1
    FROM qa_lifecycle_receipts
    WHERE phase = 'activate' AND t0_utc = NEW.t0_utc
  ) THEN
    RAISE EXCEPTION 'finalize receipt requires an activate receipt with matching T0';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_qa_lifecycle_receipts_insert_guard ON qa_lifecycle_receipts;
CREATE TRIGGER trg_qa_lifecycle_receipts_insert_guard
BEFORE INSERT ON qa_lifecycle_receipts
FOR EACH ROW EXECUTE FUNCTION tk_qa_lifecycle_receipts_insert_guard();

CREATE OR REPLACE FUNCTION tk_qa_hourly_cutover_setting_immutable()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.key = 'qa_hourly_storage_cutover_utc' THEN
            RAISE EXCEPTION 'qa_hourly_storage_cutover_utc is immutable';
        END IF;
        RETURN OLD;
    END IF;
    IF (OLD.key = 'qa_hourly_storage_cutover_utc'
        OR NEW.key = 'qa_hourly_storage_cutover_utc')
       AND (NEW.key IS DISTINCT FROM OLD.key
            OR NEW.value IS DISTINCT FROM OLD.value) THEN
        RAISE EXCEPTION 'qa_hourly_storage_cutover_utc is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_qa_hourly_cutover_setting_immutable ON settings;
CREATE TRIGGER trg_qa_hourly_cutover_setting_immutable
BEFORE UPDATE OR DELETE ON settings
FOR EACH ROW EXECUTE FUNCTION tk_qa_hourly_cutover_setting_immutable();
