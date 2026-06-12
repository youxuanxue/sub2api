-- Async video (POST /v1/video/generations) bills per second via the pricing overlay
-- (output_cost_per_second), but usage_logs had no duration column — the billed seconds
-- were invisible for audit / reconciliation (you could see the cost but not the quantity).
-- NULL = not a video request, or a historical video row recorded before tk_025.
-- Value = seconds the request was billed for (requested duration captured at submit,
-- default 8 when the client omits the field). Pure metering: no billing-amount change.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS video_duration_seconds BIGINT NULL;

COMMENT ON COLUMN usage_logs.video_duration_seconds IS
    'Billed video duration in seconds for async video generations (per-second billing). NULL = not a video request or row predates tk_025.';
