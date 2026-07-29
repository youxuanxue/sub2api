-- Migration: tk_067_vertex_gemini3_dispatch_model_mapping
--
-- Expand Google-Vertex (group 16) Vertex AI accounts so Claude-Code dispatch
-- targets gemini-3.6-flash / gemini-3.5-flash-lite are schedulable via
-- credentials.model_mapping. Probes confirmed both ids on accounts 47/57/58/59/74
-- (2026-07-22); tk_messages_dispatch_family_registry.json already maps Sonnet/Haiku
-- to these ids.
--
-- Merges the channel_type=41 SSOT floor subset from ops/pricing/model-surface-bundle.json
-- into live account mappings without dropping existing imagen/veo/2.5 rows.
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
                "gemini-3.6-flash": "gemini-3.6-flash",
                "gemini-3.5-flash-lite": "gemini-3.5-flash-lite"
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
