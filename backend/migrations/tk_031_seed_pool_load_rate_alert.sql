-- tk_031_seed_pool_load_rate_alert.sql
--
-- Seed the default-on "账号池容量触顶" (pool_load_rate) P0 alert rule.
--
-- Why a seeded default (CLAUDE.md §5.x point 1 — override/extend the default via
-- migration, overridable in the admin UI):
--   The 17-metric ops alert engine ships with ZERO default rules for capacity
--   saturation, so the single most important failure mode — a scheduling pool
--   approaching its concurrency ceiling — was silently un-alerted until an
--   operator hand-authored a rule (nobody did). pool_load_rate is the leading,
--   normalized, pool-level "go add accounts" signal that unifies the three
--   scenarios operators asked about (upstream surge / queueing / pool capacity
--   ceiling). It must be on by default; an operator can retune/disable/delete it.
--
-- Metric semantics (backend/internal/service/ops_pool_load_rate_tk.go):
--   LoadRate = (in-flight + queued) / Σ seats, aggregated per scheduling pool
--   = (platform, group_id, channel_type). channel_type only splits the fifth
--   platform `newapi` (deepseek/qwen/volcengine are non-substitutable upstreams).
--   The rule value is the MAX LoadRate across all eligible pools (≥2 accounts,
--   bounded concurrency); the Feishu card's 主因 line names the offending pools.
--   This is the leading "smoke detector"; the empty-pool "平台池全不可调度" P0
--   stays as the last-resort "fire alarm".
--
-- Threshold: ≥ 90% sustained 5 minutes → P0. 90% is "approaching the ceiling"
-- (>100% means already queuing); sustained 5m + 10m cooldown guard against
-- transient bursts and false-P0 noise. notify_email=true routes it to Feishu P0.
--
-- Idempotent: ON CONFLICT (name) DO NOTHING, like the 033 seeds.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '账号池容量触顶',
    '当任一调度池(平台/分组/渠道)的负载率(在途+排队)/席位 ≥ 90% 且持续 5 分钟时触发(池级容量饱和的前瞻信号,非单账号触顶噪音);需立即补号或核对席位余量。newapi 第五平台按渠道(deepseek/qwen/volcengine)分别计算。',
    true, 'pool_load_rate', '>=', 90.0, 5, 5, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
