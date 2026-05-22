-- Anthropic OAuth tiered baseline apply template
-- Source of truth: deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json
-- Usage (psql):
--   \set account_name 'your-account-name'
--   \set stability_tier 'l1'  -- l1|l2|l3|l4|l5
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
    ('l1'::text, 1::int, 10::int, 4::int, 4::int, 3::int, 8::int, 120::int, 0::int, false::boolean),
    ('l2'::text, 2::int, 20::int, 6::int, 6::int, 4::int, 8::int, 220::int, 0::int, false::boolean),
    ('l3'::text, 3::int, 30::int, 10::int, 6::int, 5::int, 8::int, 400::int, 0::int, true::boolean),
    ('l4'::text, 5::int, 40::int, 14::int, 10::int, 8::int, 8::int, 800::int, 0::int, true::boolean),
    ('l5'::text, 8::int, 50::int, 22::int, 14::int, 12::int, 8::int, 1500::int, 0::int, true::boolean)
  ) AS v(
    stability_tier,
    concurrency,
    priority,
    base_rpm,
    rpm_sticky_buffer,
    max_sessions,
    session_idle_timeout_minutes,
    window_cost_limit,
    window_cost_sticky_reserve,
    cache_ttl_override_enabled
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
        pg_temp.pg_raise('invalid stability_tier=%s (expected l1/l2/l3/l4/l5)', (SELECT stability_tier FROM input))
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
        -- 仅保留 403 account_disabled / organization_disabled 一条规则。
        -- 429 / 529 / 401 删除后由 case 429 (handle429 retry-after / 5h-window 精准 reset_at) /
        -- case 529 (handle529 + OverloadCooldownMinutes admin 全局可调) /
        -- case 401 (OAuth force-refresh + OAuth401CooldownMinutes + 二次升级 SetError) 各自接管。
        -- 403 这一条 + tryTempUnschedulable 的二次命中升级（ratelimit_service.go L2031-2042）
        -- 共同构成 "先睡 6h 再死" 的语义间隔，与 baselines-tiered.json 一致。
        'temp_unschedulable_rules',
          '[
            {"error_code":403,"keywords":["account_disabled_auth_error","organization disabled"],"duration_minutes":360,"description":"账号或组织禁用 - 暂停 6 小时（二次命中由 handle403 升级为 SetError）"}
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
        'cache_ttl_override_enabled', s.cache_ttl_override_enabled,
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
