-- Per-key supplier preference. This changes candidate ordering only; it does
-- not change account eligibility, health, capacity, or billing gates.
-- bluegreen-safe-destructive-ok: additive NOT NULL DEFAULT 'default'; prod verification found 473 api_keys rows, no existing routing_preference column, and session defaults lock_timeout=0 / statement_timeout=0. The transactional migration sets lock_timeout=5s and statement_timeout=5min, old binaries ignore the additive column, and rollback of the image leaves the additive column while a failed migration rolls back atomically.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS routing_preference VARCHAR(32) NOT NULL DEFAULT 'default';

ALTER TABLE api_keys
  DROP CONSTRAINT IF EXISTS api_keys_routing_preference_check;

ALTER TABLE api_keys
  ADD CONSTRAINT api_keys_routing_preference_check
  CHECK (routing_preference IN ('default', 'tokensea_first'));

COMMENT ON COLUMN api_keys.routing_preference IS
  'default | tokensea_first — per-key candidate ordering preference with normal fallback';
