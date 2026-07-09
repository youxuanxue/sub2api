-- tk_060_seed_user_visible_failure_alerts.sql
--
-- Experience-first Feishu coverage: root-cause alerts such as
-- routing_capacity_rejection_count and upstream_error_rate are intentionally
-- narrow. They explain why a class of failures happened, but they cannot be the
-- single guardrail for "real users are seeing terminal failures". These two
-- rules make that user-experience signal explicit:
--
--   * P0 user_visible_failure_count: provider/platform-owned terminal failures.
--     This catches final upstream 429/5xx, platform 499/5xx, routing capacity
--     failures, and any other non-client terminal failures attributable to real
--     users, including cases deliberately excluded from upstream_error_rate.
--
--   * P1 client_visible_failure_count: client-owned terminal failures. These are
--     still real customer experience failures, but the action is operational
--     follow-up/customer communication rather than platform incident repair.
--
-- Both metrics count only attributable real-user rows (user_id or deleted-key
-- owner present), status_code >= 400, and is_count_tokens=false. Recovered-200
-- rows never count.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '真实用户体验受损',
    '5 分钟内真实用户可感知的 provider/platform 终态失败累计 ≥ 20 时触发。统计 status_code>=400、error_owner IN (provider,platform)、is_count_tokens=false 且可归属真实用户的请求；不包含 recovered-200。该规则是用户体验兜底，根因由飞书卡片 breakdown 展示。',
    true, 'user_visible_failure_count', '>=', 20.0, 5, 1, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '真实用户客户端失败增多',
    '5 分钟内真实用户可感知的 client-owned 终态失败累计 ≥ 20 时触发。统计 status_code>=400、error_owner=client、is_count_tokens=false 且可归属真实用户的请求；用于运营同步客户参数、内容、权限、限额或用法问题。',
    true, 'client_visible_failure_count', '>=', 20.0, 5, 1, 'P1', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
