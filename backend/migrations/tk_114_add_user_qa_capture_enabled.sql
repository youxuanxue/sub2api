-- TK: per-user QA capture gate. Site-wide qa_capture.enabled may stay on;
-- when false, gateway QA middleware skips body/blob persistence for that user.
-- Default true keeps capture ON for all existing users (backward compatible).
-- Owner: internal/observability/qa CaptureFromContext (reads apiKey.User.QACaptureEnabled).
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS qa_capture_enabled BOOLEAN NOT NULL DEFAULT true;

COMMENT ON COLUMN users.qa_capture_enabled IS
    'Per-user QA capture switch. Default true. When false, gateway QA middleware does not store request/response bodies or QA blobs for this user''s traffic even if site-wide qa_capture.enabled is on.';
