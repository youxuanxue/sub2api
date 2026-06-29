-- Migration 134 backfilled allow_image_generation for openai/gemini/antigravity only.
-- newapi vendor groups carrying Vertex (imagen/veo) or VolcEngine Seedream accounts
-- were left at default false. Admin UI now exposes the same toggle for platform=newapi
-- (GroupsView → 图片生成计费); this one-time UPDATE backfills existing prod rows.

UPDATE groups g
SET allow_image_generation = true
WHERE g.platform = 'newapi'
  AND NOT g.allow_image_generation
  AND EXISTS (
    SELECT 1
    FROM account_groups ag
    JOIN accounts a ON a.id = ag.account_id
    WHERE ag.group_id = g.id
      AND a.deleted_at IS NULL
      AND a.channel_type IN (41, 45)
  );
