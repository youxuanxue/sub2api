-- TK: QA Phase 2 archive closeout control (design-qa-phase2-archive-closeout.md).
-- bluegreen-safe-destructive-ok: expand-only columns have stable defaults, old app
-- readers/writers ignore them, and new tables have no old-version callers.
-- Deletion-disabled: cleanup_eligible remains false until a later approved phase.
ALTER TABLE qa_archive_shards
    ADD COLUMN IF NOT EXISTS commit_etag text NULL,
    ADD COLUMN IF NOT EXISTS aggregate_record_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS aggregate_blob_ref_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS aggregate_blob_present_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS aggregate_blob_missing_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS verified_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS restore_verified_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS verification_error_code text NULL,
    ADD COLUMN IF NOT EXISTS last_reconciled_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS final_reconciled_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS cleanup_eligible boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS qa_archive_segments (
    id                    bigserial   PRIMARY KEY,
    shard_id              bigint      NOT NULL REFERENCES qa_archive_shards(id) ON DELETE NO ACTION,
    segment_id            text        NOT NULL,
    segment_kind          text        NOT NULL,
    state                 text        NOT NULL DEFAULT 'writing',
    attempt_id            text        NOT NULL,
    manifest_key          text        NOT NULL,
    records_key           text        NOT NULL,
    evidence_pack_key     text        NULL,
    evidence_index_key    text        NULL,
    record_count          bigint      NOT NULL DEFAULT 0,
    blob_ref_count        bigint      NOT NULL DEFAULT 0,
    blob_present_count    bigint      NOT NULL DEFAULT 0,
    blob_missing_count    bigint      NOT NULL DEFAULT 0,
    logical_bytes         bigint      NOT NULL DEFAULT 0,
    artifact_bytes        bigint      NOT NULL DEFAULT 0,
    checksums             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    verified_at           timestamptz NULL,
    committed_at          timestamptz NULL,
    commit_etag           text        NULL,
    verification_error_code text      NULL,
    last_error            text        NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT qa_archive_segments_shard_segment_unique UNIQUE (shard_id, segment_id),
    CONSTRAINT qa_archive_segments_kind_check CHECK (segment_kind IN ('base', 'delta')),
    CONSTRAINT qa_archive_segments_state_check CHECK (state IN ('writing', 'verified', 'committed', 'failed', 'orphaned'))
);

CREATE INDEX IF NOT EXISTS idx_qa_archive_segments_shard_state
    ON qa_archive_segments (shard_id, state, id);

CREATE TABLE IF NOT EXISTS qa_archive_segment_records (
    segment_id bigint       NOT NULL REFERENCES qa_archive_segments(id) ON DELETE CASCADE,
    created_at timestamptz  NOT NULL,
    request_id text         NOT NULL,
    PRIMARY KEY (segment_id, created_at, request_id)
);

CREATE INDEX IF NOT EXISTS idx_qa_archive_segment_records_identity
    ON qa_archive_segment_records (created_at, request_id);
