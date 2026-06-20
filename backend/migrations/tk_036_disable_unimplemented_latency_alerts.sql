-- tk_036_disable_unimplemented_latency_alerts.sql
--
-- Disable the two seeded-but-broken latency alert rules ("P95延迟过高" /
-- "P99延迟过高", metric_type p95_latency_ms / p99_latency_ms from
-- 033_ops_monitoring_vnext.sql). They shipped enabled=true but were never wired
-- into the alert evaluator's computeRuleMetric switch, so they have NEVER fired —
-- a silent dead rule that lets an operator believe latency is monitored when it
-- is not.
--
-- Why disable rather than implement (the honest fix):
--   1. They were never implemented (no evaluator case → metric returns ok=false →
--      skipped every cycle), so disabling changes nothing observable today.
--   2. The metric they reference is full-request duration (usage_logs.duration_ms;
--      see ops_repo_dashboard.go queryUsageLatency). For an LLM streaming gateway
--      that is dominated by generation length — a 30-60s opus completion is normal,
--      not a fault — so a 2000/3000ms P95/P99 threshold would fire CONSTANTLY if
--      wired. Implementing them as-is would be strictly worse than the current
--      silence (a P2 noise storm).
--   3. The signal an operator actually wants for a gateway is time-to-first-token
--      (usage_logs.first_token_ms / OpsDashboardOverview.TTFT), with thresholds set
--      from real production latency distributions. That is a deliberate,
--      data-driven effort — not a blind wiring of a misnamed full-duration metric.
--
-- This makes the enabled alert-rule set honest: every enabled rule has a working
-- evaluator path. Proper TTFT-based latency alerting can be added later as its own
-- considered change. Idempotent: the WHERE guard makes a re-run a no-op.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET enabled = false, updated_at = NOW()
WHERE metric_type IN ('p95_latency_ms', 'p99_latency_ms')
  AND enabled = true;
