-- bluegreen-safe-destructive-ok: adding long_context_pricing_enabled with a constant DEFAULT keeps old app writes compatible while the column is absent from old code paths; PostgreSQL backfills existing rows and future old-version inserts receive TRUE automatically.

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS long_context_pricing_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS model_pricing JSONB;

UPDATE groups
SET long_context_pricing_enabled = TRUE
WHERE long_context_pricing_enabled IS DISTINCT FROM TRUE;

COMMENT ON COLUMN groups.long_context_pricing_enabled IS
    'Whether token pricing selects official/preset long-context tiers; default true preserves existing long-context billing';
COMMENT ON COLUMN groups.model_pricing IS
    'Per-model group pricing overrides channel and built-in model pricing';
