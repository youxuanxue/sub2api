-- tk_035_seed_routing_capacity_rejection_alert.sql
--
-- Seed the default-on "无可用账号拒绝激增" (routing_capacity_rejection_count) P0
-- alert rule — closing the empty-pool-429 Feishu alert blind spot.
--
-- The blind spot (why this rule must exist):
--   A storm of client-visible "no available accounts" 429s (empty-pool fast-fail,
--   #575) fell into a structural gap between BOTH TokenKey P0 Feishu channels and
--   could not page anyone, no matter the volume:
--
--   • Channel A — rule/metric evaluator (ops_alert_evaluator): every ratio rule
--     (error_rate / upstream_error_rate / success_rate) reads dashboard SQL that
--     EXCLUDES is_business_limited rows from numerator AND denominator
--     (ops_repo_dashboard.go queryErrorCounts). Empty-pool 429s are tagged
--     is_business_limited=true (ops_error_logger.go), so they are invisible to
--     every ratio rule — neither error, nor success, nor in the denominator.
--
--   • Channel B — account-incident notifier: fires only when an account is
--     actually cooled/disabled (notifyAccountSchedulingBlocked), and the pool-level
--     P0 (tkPlatformPoolExhaustedCheck) requires the WHOLE platform pool
--     ListSchedulableByPlatform()==0 AND a cooldown > 60s. In a thin-pool race the
--     pool is momentarily empty at the selection instant while accounts stay
--     schedulable=true (busy, not broken) — no account is cooled, the pool is not
--     globally exhausted, so neither path is ever invoked.
--
--   tk_014 explicitly delegated capacity coverage to channel B (#516), but that
--   delegation is unsound for the thin-pool race above. This rule is the missing
--   coverage: a DIRECT count of what clients actually experienced.
--
-- Metric semantics (backend/internal/repository/ops_repo_dashboard.go +
-- backend/internal/service/ops_alert_evaluator_service.go):
--   routing_capacity_rejection_count = COUNT of ops_error_logs rows with
--   error_phase = 'routing' over the rule window. error_phase='routing' is set
--   EXCLUSIVELY for capacity rejections — the local empty-pool fast-fail (429)
--   AND relayed cc-<edge> mirror downstream-capacity rejections — so it isolates
--   them from user-level rate limits (phase upstream/request/auth). This is the
--   trailing "fire" signal that confirms saturation turned into client rejections;
--   the pool_load_rate rule (tk_031) is the leading "smoke detector", and the
--   pool-level "平台池全不可调度" P0 stays as the last-resort whole-pool alarm.
--   This rule covers the gap between them.
--
-- Threshold: >= 50 routing rejections over a 5-minute window, sustained 3 minutes
--   → P0. In a healthy fleet this count is ~0 (capacity should always be placeable),
--   so a sustained 50+/5min (~10 clients/min turned away for 3+ min) is an
--   unambiguous capacity storm. Anchor: the originating incident peaked at ~257
--   rejections/10min (~128/5min), comfortably above this line, while a brief
--   self-healing drain (the <60s cooldown-ladder regime) stays below it. window 5
--   smooths 1-min noise; sustained 3 + cooldown 15 guard against blips and
--   re-page spam. A COUNT (not a rate) is used deliberately: the symptom is "N
--   clients rejected", which matters in absolute terms and must not be diluted by
--   total traffic, and a count is self-flooring (no false P0 on low-traffic
--   windows). Operators can retune/disable/delete it in the admin UI.
--
-- notify_email=true routes it to Feishu P0. Idempotent: ON CONFLICT (name) DO
-- NOTHING, like the 033 / tk_031 seeds.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '无可用账号拒绝激增',
    '当 5 分钟内"无可用账号"路由拒绝(空池快速失败 429 + 镜像 edge 下游容量拒绝,error_phase=routing)累计 ≥ 50 次且持续 3 分钟时触发。这类客户端可见的 429 风暴被标记 is_business_limited 而排除在所有错误率/成功率指标之外,且不冷却任何账号,因此既不触发比率告警、也不触发账号级/池级 P0——本规则是唯一能看见"账号都健康但池被高负载瞬时抢空"这类拒绝风暴的信号。需立即补号/扩容或核对镜像 edge 容量。',
    true, 'routing_capacity_rejection_count', '>=', 50.0, 5, 3, 'P0', true, 15, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
