-- TokenKey: backfill OpenAI OAuth Codex fingerprint mode to device.
--
-- Runtime default is already device when the extra key is missing. This write
-- makes the choice visible in admin extra and keeps explicit off/session/full.
-- Idempotent: only fills missing, empty, or invalid values.

UPDATE accounts
SET extra = jsonb_set(
      COALESCE(extra, '{}'::jsonb),
      '{codex_fingerprint_mode}',
      '"device"'::jsonb,
      true
    ),
    updated_at = NOW()
WHERE platform = 'openai'
  AND type = 'oauth'
  AND deleted_at IS NULL
  AND COALESCE(extra->>'codex_fingerprint_mode', '') NOT IN ('off', 'device', 'session', 'full');
