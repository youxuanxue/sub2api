-- CloudWise/tokensea MaaS relays do not expose /v1/sub2api/billing; disable probe
-- noise. Repair active accounts left schedulable=false after 402 ClearError gaps.

UPDATE accounts
SET extra = (COALESCE(extra, '{}'::jsonb) - 'upstream_billing_probe')
    || jsonb_build_object('upstream_billing_probe_enabled', false),
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND (
    LOWER(TRIM(BOTH '/' FROM credentials->>'base_url')) IN (
      'https://api.cloudwise.ai/api',
      'https://api-us.cloudwise.ai/api',
      'https://agent.tokensea.ai'
    )
  );

UPDATE accounts
SET schedulable = true,
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND status = 'active'
  AND schedulable = false
  AND COALESCE(error_message, '') = ''
  AND LOWER(TRIM(BOTH '/' FROM credentials->>'base_url')) IN (
    'https://api.cloudwise.ai/api',
    'https://api-us.cloudwise.ai/api',
    'https://agent.tokensea.ai'
  );
