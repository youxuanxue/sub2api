-- Managed Batch Executor persistence (OpenAI-compatible public contract).
-- All mutable state carries a version so workers can use compare-and-swap updates.
CREATE TABLE IF NOT EXISTS batch_files (
    id VARCHAR(64) PRIMARY KEY,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT,
    purpose VARCHAR(32) NOT NULL,
    filename VARCHAR(512) NOT NULL,
    storage_ref TEXT NOT NULL,
    byte_size BIGINT NOT NULL DEFAULT 0,
    sha256 CHAR(64),
    status VARCHAR(32) NOT NULL DEFAULT 'uploaded',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS batch_files_owner_created_idx ON batch_files (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS batch_files_expires_idx ON batch_files (expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS batches (
    id VARCHAR(64) PRIMARY KEY,
    user_id BIGINT NOT NULL,
    api_key_id BIGINT,
    input_file_id VARCHAR(64) NOT NULL REFERENCES batch_files(id),
    endpoint VARCHAR(64) NOT NULL,
    completion_window VARCHAR(32) NOT NULL DEFAULT '24h',
    status VARCHAR(32) NOT NULL DEFAULT 'validating',
    request_counts JSONB NOT NULL DEFAULT '{"total":0,"completed":0,"failed":0}'::jsonb,
    output_file_id VARCHAR(64) REFERENCES batch_files(id),
    error_file_id VARCHAR(64) REFERENCES batch_files(id),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error_code VARCHAR(128),
    last_error_message TEXT,
    deadline_at TIMESTAMPTZ NOT NULL,
    retry_at TIMESTAMPTZ,
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    in_progress_at TIMESTAMPTZ,
    finalizing_at TIMESTAMPTZ,
    cancelling_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    expired_at TIMESTAMPTZ,
    failed_at TIMESTAMPTZ
);
ALTER TABLE batches ADD COLUMN IF NOT EXISTS in_progress_at TIMESTAMPTZ;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS finalizing_at TIMESTAMPTZ;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS cancelling_at TIMESTAMPTZ;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS expired_at TIMESTAMPTZ;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ;
ALTER TABLE batches ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR(255);
ALTER TABLE batches ADD COLUMN IF NOT EXISTS request_hash CHAR(64);
CREATE UNIQUE INDEX IF NOT EXISTS batches_owner_idempotency_uq ON batches (user_id, api_key_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS batches_owner_created_idx ON batches (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS batches_status_retry_idx ON batches (status, retry_at) WHERE retry_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS batches_deadline_idx ON batches (deadline_at);

CREATE TABLE IF NOT EXISTS batch_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    custom_id VARCHAR(255) NOT NULL,
    request JSONB NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    lease_owner VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    response JSONB,
    error JSONB,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    unknown_result BOOLEAN NOT NULL DEFAULT FALSE,
    provider VARCHAR(64),
    account_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    UNIQUE (batch_id, custom_id)
);
CREATE INDEX IF NOT EXISTS batch_items_claim_idx ON batch_items (status, next_attempt_at, lease_expires_at, id);
CREATE INDEX IF NOT EXISTS batch_items_batch_status_idx ON batch_items (batch_id, status);

CREATE TABLE IF NOT EXISTS batch_shards (
    id BIGSERIAL PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    shard_no INTEGER NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    item_count INTEGER NOT NULL DEFAULT 0,
    completed_count INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_retry_at TIMESTAMPTZ,
    lease_owner VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ NOT NULL,
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (batch_id, shard_no)
);
CREATE INDEX IF NOT EXISTS batch_shards_claim_idx ON batch_shards (status, next_retry_at, lease_expires_at, id);

CREATE TABLE IF NOT EXISTS batch_attempts (
    id BIGSERIAL PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    item_id BIGINT REFERENCES batch_items(id) ON DELETE CASCADE,
    shard_id BIGINT REFERENCES batch_shards(id) ON DELETE CASCADE,
    attempt_no INTEGER NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'running',
    provider VARCHAR(64),
    account_id BIGINT,
    error_code VARCHAR(128),
    error_message TEXT,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    unknown_result BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    retry_at TIMESTAMPTZ,
    UNIQUE (item_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS batch_attempts_batch_started_idx ON batch_attempts (batch_id, started_at);

CREATE TABLE IF NOT EXISTS batch_events (
    id BIGSERIAL PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    item_id BIGINT REFERENCES batch_items(id) ON DELETE CASCADE,
    shard_id BIGINT REFERENCES batch_shards(id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    event_key VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS batch_events_batch_created_idx ON batch_events (batch_id, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS batch_events_event_key_uq ON batch_events (batch_id, event_key) WHERE event_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS batch_outbox (
    id BIGSERIAL PRIMARY KEY,
    batch_id VARCHAR(64) NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    event_type VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    dedup_key VARCHAR(255),
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_by VARCHAR(128),
    locked_until TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS batch_outbox_claim_idx ON batch_outbox (status, available_at, locked_until, id);
CREATE UNIQUE INDEX IF NOT EXISTS batch_outbox_dedup_uq ON batch_outbox (dedup_key) WHERE dedup_key IS NOT NULL;
