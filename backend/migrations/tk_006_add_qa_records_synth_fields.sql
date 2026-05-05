-- issue #59 Gap 2 / docs/projects/auto-traj-from-supply-demand.md §6.1
-- Tag qa_records with the synthetic-pipeline session context emitted by the
-- M0 dual-CC client (X-Synth-Session, X-Synth-Role, X-Synth-Engineer-Level,
-- X-Synth-Pipeline). All four columns are nullable / default so the change
-- is backward-compatible with online traffic that never sets the headers
-- (per ops-unified-contract.md §2 "API changes must not break existing online callers").
ALTER TABLE qa_records
    ADD COLUMN IF NOT EXISTS synth_session_id text NULL,
    ADD COLUMN IF NOT EXISTS synth_role text NULL,
    ADD COLUMN IF NOT EXISTS synth_engineer_level text NULL,
    ADD COLUMN IF NOT EXISTS dialog_synth boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN qa_records.synth_session_id IS
    'Synthetic-pipeline session id from request header X-Synth-Session. NULL for normal (non-synth) traffic.';
COMMENT ON COLUMN qa_records.synth_role IS
    'Synthetic role (e.g. user-simulator, assistant-worker) from X-Synth-Role.';
COMMENT ON COLUMN qa_records.synth_engineer_level IS
    'Engineer level tag (e.g. P6) from X-Synth-Engineer-Level; only set for user-simulator turns.';
COMMENT ON COLUMN qa_records.dialog_synth IS
    'Quick predicate: true if the request was tagged as part of a synth dialog pipeline (X-Synth-Pipeline header present). Default false to preserve online-traffic semantics.';

-- Partial index to make `WHERE user_id = ? AND synth_session_id = ?` cheap.
-- Only indexes synth-tagged rows so it stays small for production traffic
-- where the vast majority of qa_records have synth_session_id IS NULL.
CREATE INDEX IF NOT EXISTS idx_qa_records_user_synth_session
    ON qa_records (user_id, synth_session_id, created_at)
    WHERE synth_session_id IS NOT NULL;
