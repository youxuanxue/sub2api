-- tk_112_seed_data_dependency_alerts.sql
--
-- Turn the existing per-app DB/Redis probes into first-class Feishu alerts.
-- These are dependency reachability signals from the application path; host
-- CPU/IO and data-volume runway remain owned by the data-machine watchers.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '数据机 PostgreSQL 不可用',
    '应用侧连续 2 分钟无法探活 PostgreSQL 时触发 P0 飞书告警。该信号代表网关到数据机 PostgreSQL 的依赖不可用；主机 CPU、磁盘、锁等待和 WAL 仍由数据机 watcher/容量探针补充。',
    true, 'db_unavailable_count', '>=', 1.0, 1, 2, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '数据机 Redis 不可用',
    '应用侧连续 2 分钟无法探活 Redis 时触发 P0 飞书告警。该信号代表网关到数据机 Redis 的依赖不可用；blocked clients、evictions 和内存仍由数据机 watcher/容量探针补充。',
    true, 'redis_unavailable_count', '>=', 1.0, 1, 2, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
