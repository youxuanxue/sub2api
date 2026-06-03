-- tk_014_reshape_p0_alert_rules.sql
--
-- Reshape the two default P0 alert rules seeded by 033_ops_monitoring_vnext.sql
-- to cut false-positive Feishu noise ("frequent P0 but service is fine").
--
-- Background:
--   - `成功率过低` (success_rate < 95) and `错误率极高` (error_rate > 20) are the
--     two sides of the SAME metric (success ⇔ 100 - error), so a single incident
--     double-pages on Feishu.
--   - `error_rate` already excludes is_business_limited, but STILL counts client
--     4xx (param/auth/413) and upstream 429/529 throttle as failures. In a
--     low-traffic window (e.g. 05:05 UTC) a handful of these punch the ratio
--     through the threshold even though no real outage occurred.
--
-- Override-default approach (CLAUDE.md §5.x point 1 — change the seeded default
-- via migration, do NOT mutate upstream metric SQL or Go):
--   1. Demote `成功率过低` P0 -> P1 (dedupe; still emails + shows on dashboard).
--   2. Repoint the surviving P0 from `error_rate` to `upstream_error_rate`
--      (provider-owned errors excluding 429/529 throttle; client- and gateway-
--      owned failures are error_owner != provider and are excluded too), and
--      lengthen the sustained window 1m -> 5m so a single low-traffic minute
--      can't trip it. Confirmed against the 2026-06-03T05:05Z incident: that
--      window's 68.49% was dominated by openai single-account 429 throttle +
--      recovered 503 (final 200), not a real upstream 5xx outage.
--
-- Capacity / "no available accounts" coverage is intentionally NOT added here:
-- it is owned by the account-incident Feishu channel (#516), not by these
-- request-outcome rate rules. `group_available_accounts` additionally requires a
-- per-group scope (returns ok=false unscoped, see computeRuleMetric), so it is
-- not a globally-seedable rule anyway.
--
-- Idempotent: each statement is a no-op on re-run (severity/name already changed).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 1) Dedupe: demote the success-rate framing from P0 to P1.
UPDATE ops_alert_rules
   SET severity = 'P1',
       updated_at = NOW()
 WHERE name = '成功率过低'
   AND severity = 'P0';

-- 2) Fix the numerator: switch the P0 to upstream_error_rate + a 5m window.
UPDATE ops_alert_rules
   SET name = '上游错误率极高',
       description = '当上游错误率（provider 端非 429/529 限流的错误，已排除客户端/网关侧失败与限流）超过 20% 且持续 5 分钟时触发（真实上游故障）',
       metric_type = 'upstream_error_rate',
       window_minutes = 5,
       sustained_minutes = 5,
       updated_at = NOW()
 WHERE name = '错误率极高';
