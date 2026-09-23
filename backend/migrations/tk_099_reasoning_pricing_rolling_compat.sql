-- high-risk-anchor: docs/approved/pricing-serving-single-source-of-truth.md
-- bluegreen-safe-destructive-ok: additive columns and compatibility projections;
-- legacy max prices remain readable/writable during rolling upgrades and rollback.
-- Removing the legacy numeric typmod widens its range without changing old values.
-- Upstream 239 is recorded without execution by the TK migration runner.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'channel_model_pricing'::regclass
          AND attname = 'reasoning_effort_multipliers' AND NOT attisdropped
    ) THEN
        ALTER TABLE channel_model_pricing
            ADD COLUMN reasoning_effort_multipliers JSONB NOT NULL DEFAULT '{}'::jsonb;
        UPDATE channel_model_pricing
        SET reasoning_effort_multipliers = jsonb_build_object('max', max_reasoning_effort_multiplier)
        WHERE max_reasoning_effort_multiplier IS NOT NULL;
    END IF;
END $$;

ALTER TABLE channel_account_stats_model_pricing
    ADD COLUMN IF NOT EXISTS reasoning_effort_multipliers JSONB NOT NULL DEFAULT '{}'::jsonb;

-- The generic map accepts finite positive doubles; do not round or overflow its
-- rollback projection through the old NUMERIC(10,4) limit.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = 'channel_model_pricing'::regclass
          AND attname = 'max_reasoning_effort_multiplier' AND atttypmod <> -1
          AND NOT attisdropped
    ) THEN
        ALTER TABLE channel_model_pricing
            ALTER COLUMN max_reasoning_effort_multiplier TYPE numeric;
    END IF;
END $$;

-- Keep a single effective max price, whichever application generation writes it.
-- A changed generic map wins if both fields are supplied. Clearing it clears max.
CREATE OR REPLACE FUNCTION tk_sync_channel_reasoning_max() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.reasoning_effort_multipliers = '{}'::jsonb
           AND NEW.max_reasoning_effort_multiplier IS NOT NULL THEN
            NEW.reasoning_effort_multipliers = jsonb_build_object('max', NEW.max_reasoning_effort_multiplier);
        END IF;
    ELSIF NEW.reasoning_effort_multipliers IS NOT DISTINCT FROM OLD.reasoning_effort_multipliers
          AND NEW.max_reasoning_effort_multiplier IS DISTINCT FROM OLD.max_reasoning_effort_multiplier THEN
        NEW.reasoning_effort_multipliers = NEW.reasoning_effort_multipliers - 'max';
        IF NEW.max_reasoning_effort_multiplier IS NOT NULL THEN
            NEW.reasoning_effort_multipliers = NEW.reasoning_effort_multipliers ||
                jsonb_build_object('max', NEW.max_reasoning_effort_multiplier);
        END IF;
    END IF;
    NEW.max_reasoning_effort_multiplier = (NEW.reasoning_effort_multipliers->>'max')::numeric;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER tk_channel_reasoning_max_compat
BEFORE INSERT OR UPDATE OF reasoning_effort_multipliers, max_reasoning_effort_multiplier
ON channel_model_pricing FOR EACH ROW EXECUTE FUNCTION tk_sync_channel_reasoning_max();

-- Also repairs databases that already ran 239: an explicit empty map stays empty.
UPDATE channel_model_pricing
SET max_reasoning_effort_multiplier = (reasoning_effort_multipliers->>'max')::numeric
WHERE max_reasoning_effort_multiplier IS DISTINCT FROM (reasoning_effort_multipliers->>'max')::numeric;

CREATE OR REPLACE FUNCTION tk_sync_group_reasoning_max() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    entry jsonb;
    normalized jsonb = '[]'::jsonb;
BEGIN
    IF jsonb_typeof(NEW.model_pricing) IS DISTINCT FROM 'array' THEN
        RETURN NEW;
    END IF;
    FOR entry IN SELECT value FROM jsonb_array_elements(NEW.model_pricing) LOOP
        IF jsonb_typeof(entry) = 'object' THEN
            IF entry ? 'reasoning_effort_multipliers' THEN
                entry = entry - 'max_reasoning_effort_multiplier';
                IF jsonb_typeof(entry->'reasoning_effort_multipliers'->'max') = 'number' THEN
                    entry = entry || jsonb_build_object('max_reasoning_effort_multiplier',
                        entry->'reasoning_effort_multipliers'->'max');
                END IF;
            ELSIF jsonb_typeof(entry->'max_reasoning_effort_multiplier') = 'number' THEN
                entry = entry || jsonb_build_object('reasoning_effort_multipliers',
                    jsonb_build_object('max', entry->'max_reasoning_effort_multiplier'));
            END IF;
        END IF;
        normalized = normalized || jsonb_build_array(entry);
    END LOOP;
    NEW.model_pricing = normalized;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER tk_group_reasoning_max_compat
BEFORE INSERT OR UPDATE OF model_pricing ON groups
FOR EACH ROW EXECUTE FUNCTION tk_sync_group_reasoning_max();

UPDATE groups SET model_pricing = model_pricing
WHERE jsonb_typeof(model_pricing) = 'array' AND model_pricing <> '[]'::jsonb;
