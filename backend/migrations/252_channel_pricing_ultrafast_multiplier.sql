-- bluegreen-safe-destructive-ok: expand-only columns with constant defaults; old app ignores them; keep columns on rollback.
-- 2026-11-18 read-only evidence: prod/cn-prod groups=114/12, channel pricing=0/0, rollup state=1/1 (PG 18.6).
SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS ultrafast_multiplier NUMERIC(12,6);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'channel_model_pricing_ultrafast_multiplier_positive'
          AND conrelid = 'channel_model_pricing'::regclass
    ) THEN
        ALTER TABLE channel_model_pricing
            ADD CONSTRAINT channel_model_pricing_ultrafast_multiplier_positive
            CHECK (ultrafast_multiplier IS NULL OR ultrafast_multiplier > 0);
    END IF;
END $$;

COMMENT ON COLUMN channel_model_pricing.ultrafast_multiplier IS
    'Optional channel-specific price multiplier for OpenAI ultrafast service tier; NULL uses the model default';
