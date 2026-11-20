-- TK: QA raw archive shard control rows (design-prod-qa-24h-s3-lifecycle.md §14.1).
-- Phase 2 archive-only: tracks hourly windows; cleanup remains disabled until Phase 4.
CREATE TABLE IF NOT EXISTS qa_archive_shards (
    id                 bigserial    PRIMARY KEY,
    window_start       timestamptz  NOT NULL,
    window_end         timestamptz  NOT NULL,
    generation         integer      NOT NULL DEFAULT 0,
    state              text         NOT NULL DEFAULT 'pending',
    record_count       bigint       NOT NULL DEFAULT 0,
    blob_ref_count     bigint       NOT NULL DEFAULT 0,
    blob_present_count bigint       NOT NULL DEFAULT 0,
    blob_missing_count bigint       NOT NULL DEFAULT 0,
    logical_bytes      bigint       NOT NULL DEFAULT 0,
    artifact_bytes     bigint       NOT NULL DEFAULT 0,
    checksums          jsonb        NOT NULL DEFAULT '{}'::jsonb,
    s3_prefix          text         NOT NULL DEFAULT '',
    manifest_key       text         NULL,
    commit_key         text         NULL,
    first_attempt_at   timestamptz  NULL,
    completed_at       timestamptz  NULL,
    last_error         text         NULL,
    created_at         timestamptz  NOT NULL DEFAULT now(),
    updated_at         timestamptz  NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_qa_archive_shards_window_generation
    ON qa_archive_shards (window_start, generation);

CREATE INDEX IF NOT EXISTS idx_qa_archive_shards_state_window
    ON qa_archive_shards (state, window_start);
