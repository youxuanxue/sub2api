-- tk_113_seed_capacity_scaleout_alerts.sql
--
-- Seed one OPC-facing Feishu P0 for multi-app fleets:
--   gateway_inflight_usage_percent ≥ 95% sustained 2m — near hard shed (503).
--
-- OPC only pages on P0 (Feishu delivery already drops ordinary P1). Account-pool
-- saturation stays owned by the existing「账号池容量触顶」pool_load_rate ≥90% P0
-- (tk_031). No P1 leading/expansion seeds here.
--
-- Non-goals: entry Caddy 5xx rates, data-plane lock/WAL, 429 classification
-- continuous rules (still probe/triage).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '应用机 in-flight 触顶风险',
    '当任一应用机网关 relay in-flight 相对 max_in_flight_requests（默认 512）占用率 ≥ 95% 且持续 2 分钟时触发 P0 飞书告警。多应用机取各节点发布样本的最大值；临近进程级 503 甩载门闩，应立刻扩应用机或降低入站并发。',
    true, 'gateway_inflight_usage_percent', '>=', 95.0, 1, 2, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
