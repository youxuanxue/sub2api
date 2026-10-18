-- One predicate for counts and samples. Disabled resources remain visible as
-- dormant inventory, but only active users/keys/groups can suffer a live 403.
WITH orphans AS (
  SELECT k.id, k.user_id, coalesce(u.email, '') AS email, k.group_id, g.name,
         k.status = 'active' AND u.status = 'active' AND g.status = 'active' AS active
  FROM api_keys k
  JOIN groups g ON g.id = k.group_id AND g.deleted_at IS NULL
  JOIN users u ON u.id = k.user_id AND u.deleted_at IS NULL
  WHERE k.deleted_at IS NULL AND k.group_id IS NOT NULL AND g.is_exclusive = true
    AND g.subscription_type IS DISTINCT FROM 'subscription'
    AND NOT EXISTS (SELECT 1 FROM user_allowed_groups a
                    WHERE a.user_id = k.user_id AND a.group_id = k.group_id)
), details AS (
  SELECT * FROM orphans WHERE active ORDER BY user_id, id LIMIT 50
)
SELECT 'orphans=' || count(*) FROM orphans WHERE active
UNION ALL
SELECT 'dormant_orphans=' || count(*) FROM orphans WHERE NOT active
UNION ALL
SELECT 'orphan_detail|' || id || '|' || user_id || '|' || email || '|' || group_id || '|' || name
FROM details;
