-- bluegreen-safe-destructive-ok: expand-only table and trigger replacement;
-- coordinated stop/migrate/start only: old readers must not run with this trigger.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS usage_group_rollup_dirty_days (
    timezone_name TEXT NOT NULL,
    bucket_date DATE NOT NULL,
    touched_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (timezone_name, bucket_date)
);

CREATE OR REPLACE FUNCTION invalidate_group_usage_rollup_state_after_insert()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_date DATE;
    published_before DATE;
    configured_timezone TEXT := current_setting('TimeZone');
BEGIN
    SELECT MIN((created_at AT TIME ZONE configured_timezone)::date)
    INTO affected_date
    FROM inserted_usage_logs
    WHERE group_id IS NOT NULL;

    IF affected_date IS NULL THEN
        RETURN NULL;
    END IF;

    -- Historical mutations still serialize with publication, including dates
    -- in the interval about to be published rather than only the old watermark.
    IF affected_date < (statement_timestamp() AT TIME ZONE configured_timezone)::date THEN
        SELECT closed_before INTO published_before
        FROM usage_group_rollup_state WHERE id = 1 FOR UPDATE;
        IF published_before > affected_date THEN
            UPDATE usage_group_rollup_state
            SET closed_before = LEAST(closed_before, affected_date), updated_at = NOW()
            WHERE id = 1;
        END IF;
    END IF;

    -- DO UPDATE holds the date-row lock until the usage transaction commits.
    -- DO NOTHING could let a builder remove a marker for an invisible insert.
    INSERT INTO usage_group_rollup_dirty_days (timezone_name, bucket_date)
    SELECT DISTINCT configured_timezone, (created_at AT TIME ZONE configured_timezone)::date
    FROM inserted_usage_logs WHERE group_id IS NOT NULL
    ORDER BY 1, 2
    ON CONFLICT (timezone_name, bucket_date)
    DO UPDATE SET touched_at = clock_timestamp();
    RETURN NULL;
END;
$$;
