-- TK: pre-flight balance HOLD ledger for the concurrent-overdraft fix.
--
-- Why: balance is deducted AFTER the upstream serves the request (post-hoc),
-- and the deduct SQL has no `balance >= amount` floor. N concurrent requests
-- from a barely-positive balance all pass admission, all get served, then all
-- deduct — driving the balance arbitrarily negative. There is no way to
-- "un-serve" an already-served request, so the only fix is to RESERVE an
-- upper-bound estimate of the cost BEFORE forwarding (atomic `balance >= hold`
-- guard) and RELEASE it when the request ends. Actual post-hoc billing is
-- unchanged. Invariant: with hold >= actual, Σholds <= balance at admission,
-- so final balance = B - Σactual >= 0 — provably never negative.
--
-- This table is the durable hold ledger so a reserve/release pair survives a
-- crash and a reconciler can refund leaked holds (a process crash between
-- reserve and the request-end release would otherwise leave balance reduced
-- with no matching bill).
--
--   request_id  the usage-billing request id (same value RecordUsage persists
--               as usage_logs.request_id) — the reserve/release idempotency key
--               and the reconciler's cross-check anchor against the billed row.
--   amount      USD reserved (> 0); released verbatim at request end.
--   created_at  set at reserve; the reconciler refunds rows older than its TTL.
CREATE TABLE IF NOT EXISTS usage_holds (
    request_id  TEXT PRIMARY KEY,
    user_id     BIGINT      NOT NULL,
    api_key_id  BIGINT      NOT NULL,
    amount      NUMERIC     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The reconciler scans by age (oldest leaked holds first); a plain btree on
-- created_at keeps that sweep index-only on the hot path.
CREATE INDEX IF NOT EXISTS idx_usage_holds_created_at ON usage_holds (created_at);

COMMENT ON TABLE usage_holds IS
    'Pre-flight balance reservations (TK overdraft fix): a row exists only while a request is in flight; released (deleted) at request end or refunded by the hold reconciler after TTL. See tk_026.';
