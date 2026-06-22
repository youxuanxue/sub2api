-- Migration: tk_043_prune_antigravity_structural_dead_mapping
--
-- Remove PR #921-confirmed structural-dead Antigravity request aliases from
-- persisted account credentials.model_mapping. These aliases either remap away
-- from retired/deprecated Antigravity IDs or are stale preview spellings; keeping
-- them in the DB widens the visible/schedulable surface without adding a real
-- servable model. DefaultAntigravityModelMapping keeps compatibility remaps for
-- legacy clients; canonical persisted accounts should carry only live target ids.
--
-- Raw SQL account mutations bypass Ent hooks, so enqueue account_changed events
-- for the scheduler snapshot refresh (same pattern as tk_020/tk_024/tk_037).
-- Idempotent: after the first run, no row contains any key in remove_keys.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH params(remove_keys) AS (
    VALUES (ARRAY[
        'gemini-2.5-flash-image-preview',
        'gemini-3-flash-preview',
        'gemini-3-pro-high',
        'gemini-3-pro-image-preview',
        'gemini-3-pro-low',
        'gemini-3-pro-preview',
        'gemini-3.1-pro-high',
        'gemini-3.1-pro-preview'
    ]::text[])
),
upd AS (
    UPDATE accounts AS a
    SET credentials = jsonb_set(
            COALESCE(a.credentials, '{}'::jsonb),
            '{model_mapping}',
            (a.credentials -> 'model_mapping') - p.remove_keys,
            true
        ),
        updated_at = NOW()
    FROM params AS p
    WHERE a.platform = 'antigravity'
      AND a.deleted_at IS NULL
      AND jsonb_typeof(a.credentials -> 'model_mapping') = 'object'
      AND (a.credentials -> 'model_mapping') ?| p.remove_keys
    RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
