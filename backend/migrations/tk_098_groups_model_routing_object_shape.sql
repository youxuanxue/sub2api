-- Migration: tk_098_groups_model_routing_object_shape
-- Ent Group.ModelRouting is map[string][]int64 (JSON object). A JSON array
-- (including []) makes every Group Scan fail, so ListActiveGroups cannot load
-- and universal-key routing returns 500 fleet-wide.
-- high-risk-anchor: coerce array→object then CHECK object|null (tk_098)

UPDATE groups
SET model_routing = '{}'::jsonb,
    updated_at = NOW()
WHERE model_routing IS NOT NULL
  AND jsonb_typeof(model_routing) = 'array';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'groups_model_routing_object_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_model_routing_object_check
            CHECK (model_routing IS NULL OR jsonb_typeof(model_routing) = 'object');
    END IF;
END $$;
