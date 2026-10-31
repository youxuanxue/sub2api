CREATE TABLE IF NOT EXISTS feedback_tickets (
    id BIGSERIAL PRIMARY KEY,
    ticket_no VARCHAR(32) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id),
    category VARCHAR(32) NOT NULL,
    subject VARCHAR(120) NOT NULL,
    content TEXT NOT NULL,
    request_id VARCHAR(128),
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ,
    CONSTRAINT feedback_tickets_category_check CHECK (category IN ('api', 'billing', 'technical', 'other')),
    CONSTRAINT feedback_tickets_status_check CHECK (status IN ('open', 'processing', 'resolved', 'closed'))
);
CREATE INDEX IF NOT EXISTS idx_feedback_tickets_user_created ON feedback_tickets (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_feedback_tickets_status_created ON feedback_tickets (status, created_at DESC);
