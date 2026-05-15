-- Anthropic OAuth tiered baseline apply template
-- Source of truth: deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
-- Usage (psql):
--   \set account_name 'your-account-name'
--   \set stability_tier 'l1_novice'  -- l1_novice|l2_junior|l3_mid|l4_senior|l5_ultra
--   \i deploy/aws/stage0/anthropic-oauth-stability-tiered-apply-template.sql

-- Helper for explicit failure (session-scoped, no persistent schema changes).
CREATE OR REPLACE FUNCTION pg_temp.pg_raise(fmt text, val text)
RETURNS text AS $$
BEGIN
  RAISE EXCEPTION USING MESSAGE = format(fmt, val);
END;
$$ LANGUAGE plpgsql;

BEGIN;

WITH input AS (
  SELECT
    :'account_name'::text AS account_name,
    :'stability_tier'::text AS stability_tier
),
tier_cfg AS (
  SELECT *
  FROM (VALUES
    ('l1_novice'::text, 2::int, 100::int, 3::int, 1::int, 2::int, 6::int, 90::int, 1::int),
    ('l2_junior'::text, 3::int, 80::int, 6::int, 2::int, 3::int, 8::int, 180::int, 2::int),
    ('l3_mid'::text, 6::int, 40::int, 10::int, 3::int, 6::int, 10::int, 300::int, 3::int),
    ('l4_senior'::text, 7::int, 20::int, 12::int, 4::int, 7::int, 11::int, 360::int, 4::int),
    ('l5_ultra'::text, 8::int, 10::int, 14::int, 5::int, 8::int, 12::int, 420::int, 5::int)
  ) AS v(
    stability_tier,
    concurrency,
    priority,
    base_rpm,
    rpm_sticky_buffer,
    max_sessions,
    session_idle_timeout_minutes,
    window_cost_limit,
    window_cost_sticky_reserve
  )
),
selected AS (
  SELECT c.*
  FROM tier_cfg c
  JOIN input i ON c.stability_tier = i.stability_tier
),
validate AS (
  SELECT
    CASE
      WHEN NOT EXISTS (SELECT 1 FROM selected) THEN
        pg_temp.pg_raise('invalid stability_tier=%s (expected l1_novice/l2_junior/l3_mid/l4_senior/l5_ultra)', (SELECT stability_tier FROM input))
      ELSE 'ok'
    END AS ok
),
profile AS (
  INSERT INTO tls_fingerprint_profiles (
    name, description, enable_grease,
    cipher_suites, curves, point_formats, signature_algorithms,
    alpn_protocols, supported_versions, key_share_groups, psk_modes, extensions,
    created_at, updated_at
  ) VALUES (
    'claude_cli_2_1_142_node24_20260515',
    'Captured from real Claude Code CLI 2.1.142 request to https://tls.sub2api.org:8090 on 2026-05-15. runtime=node v24.3.0 darwin/arm64. ja3_hash=d871d02cecbde59abbf8f4806134addf. ja4=t13d1714h1_5b57614c22b0_43ade6aba3df.',
    false,
    '[4865,4866,4867,49195,49199,49196,49200,52393,52392,49161,49171,49162,49172,156,157,47,53]'::jsonb,
    '[29,23,24]'::jsonb,
    '[0]'::jsonb,
    '[1027,2052,1025,1283,2053,1281,2054,1537,513]'::jsonb,
    '["http/1.1"]'::jsonb,
    '[772,771]'::jsonb,
    '[29]'::jsonb,
    '[1]'::jsonb,
    '[0,23,65281,10,11,35,16,5,13,18,51,45,43,21]'::jsonb,
    NOW(), NOW()
  )
  ON CONFLICT (name) DO UPDATE SET
    description = EXCLUDED.description,
    enable_grease = EXCLUDED.enable_grease,
    cipher_suites = EXCLUDED.cipher_suites,
    curves = EXCLUDED.curves,
    point_formats = EXCLUDED.point_formats,
    signature_algorithms = EXCLUDED.signature_algorithms,
    alpn_protocols = EXCLUDED.alpn_protocols,
    supported_versions = EXCLUDED.supported_versions,
    key_share_groups = EXCLUDED.key_share_groups,
    psk_modes = EXCLUDED.psk_modes,
    extensions = EXCLUDED.extensions,
    updated_at = NOW()
  RETURNING id
),
target AS (
  SELECT a.id, a.name
  FROM accounts a
  JOIN input i ON a.name = i.account_name
  WHERE a.platform = 'anthropic'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  ORDER BY a.id
  LIMIT 1
),
updated AS (
  UPDATE accounts a
  SET
    proxy_id = NULL,
    load_factor = NULL,
    concurrency = s.concurrency,
    priority = s.priority,
    rate_multiplier = 1.0,
    auto_pause_on_expired = true,
    channel_type = 0,
    credentials = COALESCE(a.credentials, '{}'::jsonb) ||
      jsonb_build_object(
        'intercept_warmup_requests', true,
        'temp_unschedulable_enabled', true,
        'temp_unschedulable_rules',
          '[
            {"error_code":429,"keywords":["rate limit","too many requests"],"duration_minutes":30,"description":"触发限流 - 暂停 30 分钟"},
            {"error_code":529,"keywords":["overloaded","capacity"],"duration_minutes":15,"description":"上游过载 - 暂停 15 分钟"},
            {"error_code":401,"keywords":["invalid_token","expired"],"duration_minutes":30,"description":"凭据异常 - 暂停 30 分钟"},
            {"error_code":403,"keywords":["account_disabled_auth_error","organization disabled"],"duration_minutes":360,"description":"账号或组织禁用 - 暂停 6 小时"}
          ]'::jsonb
      ),
    extra = (
      COALESCE(a.extra, '{}'::jsonb)
      - 'custom_base_url'
      || jsonb_build_object(
        'enable_tls_fingerprint', true,
        'rpm_strategy', 'tiered',
        'user_msg_queue_mode', 'serialize',
        'session_id_masking_enabled', true,
        'cache_ttl_override_enabled', true,
        'cache_ttl_override_target', '1h',
        'custom_base_url_enabled', false,
        'tls_fingerprint_profile_id', (SELECT id FROM profile),
        'base_rpm', s.base_rpm,
        'rpm_sticky_buffer', s.rpm_sticky_buffer,
        'max_sessions', s.max_sessions,
        'session_idle_timeout_minutes', s.session_idle_timeout_minutes,
        'window_cost_limit', s.window_cost_limit,
        'window_cost_sticky_reserve', s.window_cost_sticky_reserve,
        'stability_tier', s.stability_tier
      )
    ),
    updated_at = NOW()
  FROM target t, selected s
  WHERE a.id = t.id
  RETURNING a.id, a.name, s.stability_tier
)
SELECT * FROM updated;

COMMIT;
