-- TK migration 028: allow 'grok' (seventh platform) in model_availability.platform CHECK constraint.
--
-- Why a new migration instead of editing tk_009/tk_016:
--   Those have already been applied on prod/edge nodes. TokenKey's migration runner enforces a
--   checksum guard — editing an already-applied migration changes its checksum and makes every
--   node refuse to start (and the deploy trap then rolls back to the prior bad image, pinning the
--   node). So the platform-set extension MUST land as a NEW, idempotent migration. Mirrors tk_016
--   (which added 'kiro' the same way).
--
-- Symptom this fixes: every grok gateway forward would log `pricing.availability.record_failed`
--   with `ent: validator failed ... invalid enum value for platform field: "grok"`. The enum gap
--   is two-layered — the ent Go PlatformValidator (regenerated via `go generate ./ent`) AND this
--   DB CHECK. This migration closes the DB layer; the ent layer closes via the schema edit
--   (ent/schema/model_availability.go Values(...,"grok")).
--
-- Idempotent: DROP CONSTRAINT IF EXISTS + re-ADD with the full platform set. Safe to re-run.

ALTER TABLE model_availability
    DROP CONSTRAINT IF EXISTS model_availability_platform_check;

ALTER TABLE model_availability
    ADD CONSTRAINT model_availability_platform_check
    CHECK (platform IN ('openai', 'anthropic', 'gemini', 'antigravity', 'newapi', 'kiro', 'grok'));
