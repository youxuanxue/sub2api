-- tk_019_dedupe_error_rate_alert.sql
--
-- Finish the de-duplication that tk_014_reshape_p0_alert_rules.sql started, and
-- fix the remaining false-positive Feishu P1 noise ("一个故障收两条 P1 + 客户端
-- 噪声误触发").
--
-- Background:
--   tk_014 demoted `成功率过低` P0 -> P1 and repointed the surviving P0 from
--   `error_rate` to the cleaned `upstream_error_rate`, BUT left BOTH the
--   `错误率过高` (error_rate > 5, P1) and `成功率过低` (success_rate < 95, P1)
--   rules enabled. Since success_rate ⇔ 100 - error_rate, those two are the two
--   sides of the SAME metric and still double-page on Feishu for every incident
--   (confirmed on prod ops_alert_events: paired same-second fires, e.g.
--   error_rate 12.50% ↔ success_rate 86.05%).
--
--   Worse, `错误率过高` still uses the RAW `error_rate` numerator
--   (ops_repo_dashboard.go: error_sla = status>=400 AND NOT is_business_limited),
--   which counts client 4xx (param/auth/413), upstream 429/529 throttle, and
--   context-canceled as "errors". In a low-traffic window a handful of these
--   punch the 5% threshold even though no real outage occurred (e.g. a 5.13%
--   blip that self-resolved in 1 minute).
--
-- Override-default approach (CLAUDE.md §5.x point 1 — change the seeded default
-- via migration; do NOT mutate the upstream metric SQL / Go):
--   1. Dedupe: DISABLE the redundant `成功率过低` rule. The error-framed rule
--      below now carries the single P1 signal; success_rate is its raw inverse
--      and is exactly the client-noise-polluted framing we want to drop.
--   2. Fix the numerator: repoint `错误率过高` from raw `error_rate` to the
--      cleaned `upstream_error_rate` (error_owner='provider', excludes client/
--      gateway failures and 429/529 throttle — the #628 cleaning already lives
--      in that metric) at an 8% threshold, forming a P1@8% -> P0@20% ladder with
--      the existing `上游错误率极高` (upstream_error_rate > 20, P0) rule. Rename
--      to `上游错误率偏高` so the ladder reads cleanly on the dashboard.
--
-- Trade-off (accepted): the P1 no longer fires on pure client 4xx/429/cancel
-- noise (intended) NOR on pure gateway-owned 5xx (error_owner='gateway', rare —
-- covered by the memory/cpu/latency/concurrency rules). Adding gateway-5xx
-- coverage would need a new metric/rule and is intentionally out of scope here.
--
-- Persistence: ops_alert_rules rows are seeded ONLY by 033_ops_monitoring_vnext
-- (INSERT ... ON CONFLICT (name) DO NOTHING) — there is NO Go-side startup
-- reseed (repository CreateAlertRule is the admin-UI manual-create path only),
-- so the disable/repoint below is durable across redeploys.
--
-- Idempotent: each UPDATE is scoped so a re-run matches 0 rows once applied.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 1) Dedupe: disable the redundant success-rate framing (raw inverse of
--    error_rate). The error-framed P1 below is the single surviving signal.
UPDATE ops_alert_rules
   SET enabled = false,
       updated_at = NOW()
 WHERE name = '成功率过低'
   AND enabled = true;

-- 1b) Resolve any still-firing event for the rule we just disabled. The
--     evaluator skips disabled rules (ops_alert_evaluator_service.go gates the
--     "resolve active event" path behind `rule.Enabled`), so an event that
--     happened to be firing at disable-time would otherwise stay 'firing'
--     forever (orphaned on the dashboard, no resolve notification). Resolving
--     here keeps the state machine consistent. Idempotent: 0 rows once applied.
UPDATE ops_alert_events
   SET status = 'resolved',
       resolved_at = NOW()
 WHERE status = 'firing'
   AND rule_id IN (SELECT id FROM ops_alert_rules WHERE name = '成功率过低');

-- 2) Fix the numerator + form the P1->P0 ladder: repoint the P1 from raw
--    error_rate to cleaned upstream_error_rate at 8%.
UPDATE ops_alert_rules
   SET name = '上游错误率偏高',
       description = '上游错误率（provider 端非 429/529 限流错误，已排除客户端/网关侧失败与限流）超过 8% 且持续 5 分钟触发（P0 阈值 20% 的早期预警）',
       metric_type = 'upstream_error_rate',
       threshold = 8.0,
       window_minutes = 5,
       sustained_minutes = 5,
       updated_at = NOW()
 WHERE name = '错误率过高'
   AND metric_type = 'error_rate';
