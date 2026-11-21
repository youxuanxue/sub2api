-- Exclude admin-role accounts from the "真实用户*" Feishu alert descriptions so
-- operators see the same numerator semantics as buildUserVisibleFailureWhere
-- (ops/probe traffic such as admin@tokenkey.dev stays in raw logs only).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET
  description = 'prod 节点 5 分钟内真实用户可感知的 provider/platform 终态失败累计 ≥ 50 时触发。统计 status_code>=400、error_owner IN (provider,platform)、is_count_tokens=false 且可归属真实用户（排除 users.role=admin 的运维/探针账号）的请求；不包含 recovered-200。edge 节点运行时跳过该规则，避免和 prod 聚合线重复。该规则是用户体验兜底，根因由飞书卡片 breakdown 展示。',
  updated_at = NOW()
WHERE name = '真实用户体验受损'
  AND metric_type = 'user_visible_failure_count';

UPDATE ops_alert_rules
SET
  description = 'prod 节点 5 分钟内真实用户 client-owned 终态失败累计 ≥ 50 且持续 5 分钟时触发 P1 飞书告警。排除客户端主动取消（499/context canceled）与 users.role=admin 的运维/探针账号；用于了解持续、大量的客户端请求错误。',
  updated_at = NOW()
WHERE name = '真实用户客户端失败增多'
  AND metric_type = 'client_visible_failure_count';
