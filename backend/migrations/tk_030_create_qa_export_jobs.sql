-- TK: durable lifecycle for trajectory export jobs (#792 follow-up).
--
-- Why: the trajectory export job status used to live ONLY in an in-memory map
-- on the qa.Service. Every prod redeploy (there are several per day) wiped that
-- map, so a finished export's download link vanished from the UI even though
-- the zip itself survived on disk — the operator saw "导出中…" reset to "导出"
-- with no way to re-find the artifact. Persisting the job here makes the
-- "my exports" panel and the download survive restart/redeploy; a startup
-- reconciler fails any row left running by the previous process.
--
-- Two kinds:
--   manual — a user clicked "立即导出"; job_id is a UUID.
--   auto   — the daily per-(user,key) archive cron; job_id is deterministic
--            ("auto:<user>:<key>:<YYYY-MM-DD>") so a same-day re-run UPSERTs the
--            same row instead of duplicating (see EnqueueAutoExport).
--
-- The zip bytes live in the blob store (localfs, or S3 under
-- traj-exports/<user_id>/<api_key_id>/... when export_storage is configured);
-- this row only tracks status + metadata + the storage_key used to build the
-- download URL. expires_at mirrors the presigned-URL / S3-lifecycle TTL.
CREATE TABLE IF NOT EXISTS qa_export_jobs (
    id            bigserial   PRIMARY KEY,
    job_id        text        NOT NULL,
    user_id       bigint      NOT NULL,
    api_key_id    bigint      NULL,
    status        text        NOT NULL DEFAULT 'pending',
    export_kind   text        NOT NULL DEFAULT 'manual',
    format        text        NOT NULL DEFAULT 'v2',
    window_start  timestamptz NULL,
    window_end    timestamptz NULL,
    storage_key   text        NOT NULL DEFAULT '',
    record_count  integer     NOT NULL DEFAULT 0,
    error         text        NULL,
    expires_at    timestamptz NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- job_id is the public identifier the frontend polls on; unique so the auto
-- cron's deterministic id gives free per-day idempotency (ON CONFLICT job_id).
CREATE UNIQUE INDEX IF NOT EXISTS idx_qa_export_jobs_job_id
    ON qa_export_jobs (job_id);

-- "my exports" panel: newest-first listing per user, and per (user, key).
CREATE INDEX IF NOT EXISTS idx_qa_export_jobs_user_created
    ON qa_export_jobs (user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_qa_export_jobs_user_key_created
    ON qa_export_jobs (user_id, api_key_id, created_at);

-- housekeeping: expire/clean finished jobs by TTL.
CREATE INDEX IF NOT EXISTS idx_qa_export_jobs_expires_at
    ON qa_export_jobs (expires_at);

COMMENT ON TABLE qa_export_jobs IS
    'Durable trajectory-export job lifecycle (manual + daily auto archive). Survives redeploy so the download link does not vanish. See tk_030 / #792 follow-up.';
