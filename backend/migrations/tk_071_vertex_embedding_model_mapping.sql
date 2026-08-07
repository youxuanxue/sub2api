-- Migration: tk_071_vertex_embedding_model_mapping
--
-- Enable gemini-embedding-001 on Google-Vertex (group 16) Vertex AI accounts.
-- Direct upstream :predict probes returned 200 on accounts 47/57/58/59/74
-- (2026-08-07). Merges the channel_type=41 SSOT floor subset without dropping
-- existing chat/image/video rows.
--
-- Raw-SQL account mutations bypass Ent hooks — enqueue scheduler_outbox refresh.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

WITH upd AS (
    UPDATE accounts a
    SET credentials = jsonb_set(
            a.credentials,
            '{model_mapping}',
            COALESCE(a.credentials -> 'model_mapping', '{}'::jsonb) || '{
                "gemini-embedding-001": "gemini-embedding-001"
            }'::jsonb
        ),
        updated_at = NOW()
    FROM account_groups ag
    JOIN groups g ON g.id = ag.group_id
    WHERE a.id = ag.account_id
      AND g.id = 16
      AND g.name = 'Google-Vertex'
      AND g.platform = 'newapi'
      AND a.platform = 'newapi'
      AND a.channel_type = 41
      AND a.deleted_at IS NULL
    RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
