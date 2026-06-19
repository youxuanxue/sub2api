-- Migration: tk_034_add_api_keys_routing_mode
--
-- Add api_keys.routing_mode for the "Universal Key" feature
-- (docs/approved/universal-key-routing.md):
--
--   - direct    (DEFAULT): legacy behavior — the key binds to a single fixed
--               group (api_keys.group_id) and routes to that one platform.
--   - universal: the key carries no fixed platform; per request it resolves to
--               the right backing group from the requested model + inbound
--               endpoint, spanning every group the owner is entitled to.
--
-- The column DEFAULT MUST be 'direct' so this migration does NOT flip any
-- existing key to universal. "New keys default to universal" is enforced at the
-- service layer (APIKeyService.Create), not by the column default.
ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS routing_mode VARCHAR(16) NOT NULL DEFAULT 'direct';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.constraint_column_usage
        WHERE table_name = 'api_keys' AND constraint_name = 'api_keys_routing_mode_check'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_routing_mode_check
            CHECK (routing_mode IN ('direct', 'universal'));
    END IF;
END $$;

COMMENT ON COLUMN api_keys.routing_mode IS
  'direct | universal — universal auto-routes per request across the owner''s entitlement span';
