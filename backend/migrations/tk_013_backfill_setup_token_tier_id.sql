-- tk_013: backfill tier_id for anthropic setup-token accounts carrying the
-- legacy extra.stability_tier label.
--
-- tk_012's backfill only matched `type = 'oauth'`, mirroring an over-narrow gate
-- that PR #472 introduced in ApplyTier/overlay/reconciler. Setup-token accounts
-- are subject to the same 5h window + session control (Account.IsAnthropicOAuth-
-- OrSetupToken) and are tier-eligible, so any setup-token account that carried a
-- stability_tier label under the old value-copy model was left with tier_id NULL
-- and is not tier-resolved at runtime. This idempotent backfill closes that gap.
--
-- Idempotent (only fills NULL tier_id). The legacy extra.* values are left in
-- place (no runtime gap; the runtime resolver overlays tier values, and the
-- reconciler re-asserts concurrency).
UPDATE accounts a
SET tier_id = t.id, updated_at = NOW()
FROM tiers t
WHERE a.platform = 'anthropic'
  AND a.type = 'setup-token'
  AND a.deleted_at IS NULL
  AND a.tier_id IS NULL
  AND lower(trim(a.extra->>'stability_tier')) = t.name;
