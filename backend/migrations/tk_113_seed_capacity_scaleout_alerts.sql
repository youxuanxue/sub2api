-- tk_113_seed_capacity_scaleout_alerts.sql
--
-- Seed Feishu capacity / scale-out rules for multi-app fleets:
--   1) pool_load_rate P1 at 80% — expansion signal before the existing 90% P0
--   2) gateway_inflight_usage_percent P1 at 80% — app-node relay gate approaching
--      gateway.max_in_flight_requests (default 512); fan-in across app nodes
--   3) gateway_inflight_usage_percent P0 at 95% — near hard shed (503)
--
-- Non-goals: entry Caddy 5xx rates, data-plane lock/WAL, 429 classification
-- continuous rules (still probe/triage). Existing「账号池容量触顶」P0 at 90%
-- and「并发队列积压」stay unchanged.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '账号池接近饱和',
    '当任一调度池(平台/分组/渠道)的负载率(在途+排队)/席位 ≥ 80% 且持续 5 分钟时触发 P1 飞书告警。这是「账号池容量触顶」(≥90% P0) 之前的扩容/补号前瞻信号；newapi 按渠道拆子池。',
    true, 'pool_load_rate', '>=', 80.0, 5, 5, 'P1', true, 15, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '应用机 in-flight 接近上限',
    '当任一应用机网关 relay in-flight 相对 gateway.max_in_flight_requests（默认 512）的占用率 ≥ 80% 且持续 5 分钟时触发 P1。多应用机取各节点发布样本的最大值；达到上限后新请求会被本机门闩 503 甩载。',
    true, 'gateway_inflight_usage_percent', '>=', 80.0, 1, 5, 'P1', true, 15, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '应用机 in-flight 触顶风险',
    '当任一应用机网关 relay in-flight 相对 max_in_flight_requests 占用率 ≥ 95% 且持续 2 分钟时触发 P0。临近进程级甩载门闩；应立刻扩应用机或降低入站并发，避免大面积 503。',
    true, 'gateway_inflight_usage_percent', '>=', 95.0, 1, 2, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
