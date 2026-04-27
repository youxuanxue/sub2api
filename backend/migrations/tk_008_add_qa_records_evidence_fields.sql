-- Evidence-spine fields for qa_records: scheduling context, split blob URIs,
-- redaction contract version, and capture status (trajectory export / QA observability).
-- All new columns are nullable or have safe defaults so existing rows and online
-- callers remain valid.
ALTER TABLE qa_records
    ADD COLUMN IF NOT EXISTS group_id bigint NULL,
    ADD COLUMN IF NOT EXISTS provider text NULL,
    ADD COLUMN IF NOT EXISTS channel_type integer NULL,
    ADD COLUMN IF NOT EXISTS success boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS request_blob_uri text NULL,
    ADD COLUMN IF NOT EXISTS response_blob_uri text NULL,
    ADD COLUMN IF NOT EXISTS stream_blob_uri text NULL,
    ADD COLUMN IF NOT EXISTS redaction_version text NOT NULL DEFAULT 'logredact',
    ADD COLUMN IF NOT EXISTS capture_status text NOT NULL DEFAULT 'captured';

COMMENT ON COLUMN qa_records.group_id IS
    'API key group at capture time; optional for older rows.';
COMMENT ON COLUMN qa_records.provider IS
    'Gateway routing provider label (e.g. newapi bridge), optional.';
COMMENT ON COLUMN qa_records.channel_type IS
    'New API channel type when applicable; NULL for non-newapi platforms.';
COMMENT ON COLUMN qa_records.success IS
    'True when the gateway considers the call successful (complements HTTP status_code).';
COMMENT ON COLUMN qa_records.request_blob_uri IS
    'Object storage URI for redacted request body blob.';
COMMENT ON COLUMN qa_records.response_blob_uri IS
    'Object storage URI for redacted non-streaming response body.';
COMMENT ON COLUMN qa_records.stream_blob_uri IS
    'Object storage URI for redacted streaming transcript / SSE aggregate.';
COMMENT ON COLUMN qa_records.redaction_version IS
    'Version tag for the redaction key set; must stay aligned with logredact defaults (see redaction-sentinels).';
COMMENT ON COLUMN qa_records.capture_status IS
    'Capture pipeline state (e.g. captured, partial, failed).';

CREATE INDEX IF NOT EXISTS idx_qa_records_group_id_created_at
    ON qa_records (group_id, created_at)
    WHERE group_id IS NOT NULL;
