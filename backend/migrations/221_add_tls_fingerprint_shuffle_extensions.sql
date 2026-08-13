-- Persist whether a TLS fingerprint profile permutes extension order per ClientHello.
-- bluegreen-safe-destructive-ok: expand-only NOT NULL column with a stable false
-- default; old binaries ignore it, old writers may omit it, and existing rows remain
-- behaviorally unchanged until a profile explicitly enables extension shuffling.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE tls_fingerprint_profiles
    ADD COLUMN IF NOT EXISTS shuffle_extensions BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN tls_fingerprint_profiles.shuffle_extensions IS
    'Whether to randomize TLS extension order for each ClientHello';
