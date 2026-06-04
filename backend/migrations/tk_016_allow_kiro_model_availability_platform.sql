-- TK migration 016: allow 'kiro' (sixth platform) in model_availability.platform CHECK constraint.
--
-- Why a new migration instead of editing tk_009:
--   tk_009 has already been applied on prod/edge nodes. TokenKey's migration runner enforces a
--   checksum guard — editing an already-applied migration changes its checksum and makes every
--   node refuse to start (and the deploy trap then rolls back to the prior bad image, pinning the
--   node). So the platform-set extension MUST land as a NEW, idempotent migration.
--
-- Symptom this fixes: every kiro gateway forward logged `pricing.availability.record_failed`
--   with `ent: validator failed ... invalid enum value for platform field: "kiro"`. The enum gap
--   was two-layered — the ent Go PlatformValidator (regenerated via `go generate ./ent`) AND this
--   DB CHECK. This migration closes the DB layer; the ent layer closes via the schema edit.
--
-- Idempotent: DROP CONSTRAINT IF EXISTS + re-ADD with the full platform set, mirroring the
--   135_allow_email_oauth_provider_types.sql范式. Safe to re-run.

ALTER TABLE model_availability
    DROP CONSTRAINT IF EXISTS model_availability_platform_check;

ALTER TABLE model_availability
    ADD CONSTRAINT model_availability_platform_check
    CHECK (platform IN ('openai', 'anthropic', 'gemini', 'antigravity', 'newapi', 'kiro'));
