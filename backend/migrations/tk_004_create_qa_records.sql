CREATE TABLE IF NOT EXISTS qa_records (
    id bigserial NOT NULL,
    request_id text NOT NULL,
    user_id bigint NOT NULL,
    api_key_id bigint NOT NULL,
    account_id bigint NULL,
    platform text NOT NULL DEFAULT 'unknown',
    requested_model text NOT NULL DEFAULT '',
    upstream_model text NULL,
    inbound_endpoint text NOT NULL DEFAULT '',
    upstream_endpoint text NULL,
    status_code integer NOT NULL DEFAULT 0,
    duration_ms bigint NOT NULL DEFAULT 0,
    first_token_ms bigint NULL,
    stream boolean NOT NULL DEFAULT false,
    tool_calls_present boolean NOT NULL DEFAULT false,
    multimodal_present boolean NOT NULL DEFAULT false,
    input_tokens integer NOT NULL DEFAULT 0,
    output_tokens integer NOT NULL DEFAULT 0,
    cached_tokens integer NOT NULL DEFAULT 0,
    request_sha256 text NOT NULL DEFAULT '',
    response_sha256 text NOT NULL DEFAULT '',
    blob_uri text NULL,
    tags jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    retention_until timestamptz NOT NULL,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_qa_records_request_id_created_at
    ON qa_records (request_id, created_at);

CREATE INDEX IF NOT EXISTS idx_qa_records_created_at
    ON qa_records (created_at);

CREATE INDEX IF NOT EXISTS idx_qa_records_api_key_created_at
    ON qa_records (api_key_id, created_at);

CREATE INDEX IF NOT EXISTS idx_qa_records_user_created_at
    ON qa_records (user_id, created_at);

CREATE INDEX IF NOT EXISTS idx_qa_records_platform_status_created_at
    ON qa_records (platform, status_code, created_at);

CREATE TABLE IF NOT EXISTS qa_records_default
    PARTITION OF qa_records DEFAULT;

DO $$
DECLARE
    month_start date;
    next_month_start date;
    part_name text;
BEGIN
    month_start := date_trunc('month', now())::date;
    next_month_start := (month_start + interval '1 month')::date;
    part_name := format('qa_records_%s', to_char(month_start, 'YYYY_MM'));
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF qa_records FOR VALUES FROM (%L) TO (%L)',
        part_name, month_start::text, next_month_start::text
    );

    month_start := next_month_start;
    next_month_start := (month_start + interval '1 month')::date;
    part_name := format('qa_records_%s', to_char(month_start, 'YYYY_MM'));
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF qa_records FOR VALUES FROM (%L) TO (%L)',
        part_name, month_start::text, next_month_start::text
    );
END $$;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS qa_capture_enabled boolean NOT NULL DEFAULT true;

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS qa_never_capture boolean NOT NULL DEFAULT false;
