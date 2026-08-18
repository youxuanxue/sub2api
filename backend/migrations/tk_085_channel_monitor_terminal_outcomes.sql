-- Complete gateway terminal outcomes for high-precision Edge model-unit alerts.
-- Counts are cumulative per producer epoch so retrying a flush is idempotent.
CREATE TABLE IF NOT EXISTS channel_monitor_v2_terminal_outcomes_1m (
    bucket_start TIMESTAMPTZ NOT NULL,
    group_id BIGINT NOT NULL DEFAULT 0,
    requested_model TEXT NOT NULL,
    producer_epoch TEXT NOT NULL,
    success_count BIGINT NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    final_empty_pool_429_count BIGINT NOT NULL DEFAULT 0 CHECK (final_empty_pool_429_count >= 0),
    other_error_count BIGINT NOT NULL DEFAULT 0 CHECK (other_error_count >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, group_id, requested_model, producer_epoch)
);

CREATE INDEX IF NOT EXISTS idx_channel_monitor_terminal_outcomes_model_time
    ON channel_monitor_v2_terminal_outcomes_1m (requested_model, bucket_start DESC);

CREATE TABLE IF NOT EXISTS channel_monitor_v2_terminal_ingestion_health_1m (
    bucket_start TIMESTAMPTZ NOT NULL,
    producer_epoch TEXT NOT NULL,
    seen_count BIGINT NOT NULL DEFAULT 0 CHECK (seen_count >= 0),
    persisted_count BIGINT NOT NULL DEFAULT 0 CHECK (persisted_count >= 0),
    drop_count BIGINT NOT NULL DEFAULT 0 CHECK (drop_count >= 0),
    flush_failure_count BIGINT NOT NULL DEFAULT 0 CHECK (flush_failure_count >= 0),
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, producer_epoch)
);

CREATE INDEX IF NOT EXISTS idx_channel_monitor_terminal_health_time
    ON channel_monitor_v2_terminal_ingestion_health_1m (bucket_start DESC);

CREATE OR REPLACE VIEW channel_monitor_v2_terminal_outcomes_5m AS
WITH health AS (
    SELECT
        date_bin(INTERVAL '5 minutes', bucket_start, TIMESTAMPTZ '2000-01-01 00:00:00+00') AS bucket_start,
        COUNT(DISTINCT bucket_start) AS heartbeat_minutes,
        COUNT(DISTINCT producer_epoch) AS producer_epochs,
        BOOL_AND(complete) AS all_complete,
        MAX(heartbeat_at) AS watermark
    FROM channel_monitor_v2_terminal_ingestion_health_1m
    GROUP BY 1
), facts AS (
    SELECT
        date_bin(INTERVAL '5 minutes', bucket_start, TIMESTAMPTZ '2000-01-01 00:00:00+00') AS bucket_start,
        group_id,
        requested_model,
        SUM(success_count) AS success_count,
        SUM(final_empty_pool_429_count) AS final_empty_pool_429_count,
        SUM(other_error_count) AS other_error_count
    FROM channel_monitor_v2_terminal_outcomes_1m
    GROUP BY 1, 2, 3
)
SELECT
    h.bucket_start,
    f.group_id,
    f.requested_model,
    COALESCE(f.success_count, 0) AS success_count,
    COALESCE(f.final_empty_pool_429_count, 0) AS final_empty_pool_429_count,
    COALESCE(f.other_error_count, 0) AS other_error_count,
    (h.heartbeat_minutes = 5 AND h.producer_epochs = 1 AND h.all_complete) AS complete,
    h.watermark
FROM health h
LEFT JOIN facts f USING (bucket_start);

COMMENT ON VIEW channel_monitor_v2_terminal_outcomes_5m IS
    'Closed five-minute terminal facts; complete requires five heartbeats, one producer epoch, and exact conservation.';
