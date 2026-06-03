-- Migration: tk_015_kiro_default_concurrency
-- Give every Kiro account a bounded concurrency. concurrency<=0 means "unlimited"
-- (AcquireAccountSlot treats <=0 as no limit), which removes the per-account
-- in-flight cap that protects Kiro accounts from the AWS CodeWhisperer ban
-- heuristics. The community anti-ban guidance is a single-account concurrency of
-- ~5; pin any unlimited Kiro account to 5.
--
-- Scope: ALL Kiro accounts with concurrency<=0 across every environment (prod +
-- edges + local). Accounts already at an explicit 1..N are left untouched (an
-- operator may have tuned them). New Kiro accounts keep the schema default (3),
-- which is already bounded — this migration only fixes the unlimited ones.
--
-- Raw-SQL account mutations bypass the Ent hooks that normally enqueue a
-- scheduler snapshot refresh, so enqueue one scheduler_outbox `account_changed`
-- event per bumped account (same shape as account_repo_tk_rate_limit_reaper.go)
-- in the same statement — otherwise running replicas keep the stale (unlimited)
-- value until their next full snapshot reload.
--
-- Idempotent: a re-run finds no concurrency<=0 Kiro accounts, updates 0 rows, and
-- enqueues 0 events.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH bumped AS (
    UPDATE accounts
    SET concurrency = 5,
        updated_at = NOW()
    WHERE platform = 'kiro'
      AND concurrency <= 0
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL
FROM bumped;
