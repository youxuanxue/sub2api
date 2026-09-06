-- Persist outbound Codex installation_id for OAuth egress forensics.
-- Lets operators COUNT DISTINCT devices after fingerprint convergence without
-- relying on rotated docker logs. Nullable: non-Codex / fingerprint-off rows stay NULL.
-- bluegreen-safe-destructive-ok: nullable expand-only column on usage_logs.

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS codex_installation_id VARCHAR(64);

COMMENT ON COLUMN usage_logs.codex_installation_id IS
    'Outbound Codex x-codex-installation-id after fingerprint convergence (device/session/full). NULL when not applicable (fingerprint off, non-Codex, or unmeasured).';
