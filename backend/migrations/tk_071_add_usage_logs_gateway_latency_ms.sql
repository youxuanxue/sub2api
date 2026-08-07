-- Persist per-request gateway transfer latency for admin dashboard observability.
-- gateway_latency_ms = auth + routing + response tail, excluding upstream wait, streaming pump, and queue/throttle waits.
-- bluegreen-safe-destructive-ok: nullable usage_logs column + aggregate totals with DEFAULT 0 only (expand phase).

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS gateway_latency_ms INT;

COMMENT ON COLUMN usage_logs.gateway_latency_ms IS
    'TokenKey gateway transfer latency in ms (auth+routing+response tail), excluding upstream provider inference/network wait, streaming body pump, and queue/throttle waits. NULL when not measured.';

ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS total_gateway_latency_ms BIGINT NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_hourly
    ADD COLUMN IF NOT EXISTS gateway_latency_samples BIGINT NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS total_gateway_latency_ms BIGINT NOT NULL DEFAULT 0;

ALTER TABLE usage_dashboard_daily
    ADD COLUMN IF NOT EXISTS gateway_latency_samples BIGINT NOT NULL DEFAULT 0;
