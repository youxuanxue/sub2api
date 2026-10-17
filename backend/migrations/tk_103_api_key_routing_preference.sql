-- Per-key supplier preference. This changes candidate ordering only; it does
-- not change account eligibility, health, capacity, or billing gates.
ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS routing_preference VARCHAR(32) NOT NULL DEFAULT 'default';

ALTER TABLE api_keys
  DROP CONSTRAINT IF EXISTS api_keys_routing_preference_check;

ALTER TABLE api_keys
  ADD CONSTRAINT api_keys_routing_preference_check
  CHECK (routing_preference IN ('default', 'tokensea_first'));

COMMENT ON COLUMN api_keys.routing_preference IS
  'default | tokensea_first — per-key candidate ordering preference with normal fallback';
