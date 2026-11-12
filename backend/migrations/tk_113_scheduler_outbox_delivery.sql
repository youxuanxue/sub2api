-- bluegreen-safe-destructive-ok: expand-only delivery columns and indexes;
-- old watermark cleanup is intentionally fenced. Worker cutover is coordinated.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE SEQUENCE scheduler_outbox_projection_generation AS BIGINT;

ALTER TABLE scheduler_outbox
    ADD COLUMN IF NOT EXISTS claim_token TEXT,
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_scheduler_outbox_pending_delivery
    ON scheduler_outbox (id) WHERE acknowledged_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_scheduler_outbox_acknowledged
    ON scheduler_outbox (acknowledged_at, id) WHERE acknowledged_at IS NOT NULL;

CREATE OR REPLACE FUNCTION protect_unacknowledged_scheduler_outbox()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.acknowledged_at IS NULL THEN
        RAISE EXCEPTION 'scheduler outbox event requires a delivery ACK before deletion';
    END IF;
    RETURN OLD;
END;
$$;
CREATE TRIGGER scheduler_outbox_require_ack_before_delete
    BEFORE DELETE ON scheduler_outbox
    FOR EACH ROW EXECUTE FUNCTION protect_unacknowledged_scheduler_outbox();
