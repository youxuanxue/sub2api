-- Reduce non-actionable client-fault paging. Caller cancellations are excluded
-- from the numerator in buildUserVisibleFailureWhere; this migration lowers the
-- remaining client-validation signal to an operational P2 notification.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET severity = 'P1',
    threshold = 50.0,
    window_minutes = 5,
    sustained_minutes = 5,
    notify_email = true,
    description = 'prod 节点 5 分钟内真实用户 client-owned 终态失败累计 ≥ 50 且持续 5 分钟时触发 P1 飞书告警。排除客户端主动取消（499/context canceled）；用于聚合同类请求问题并检查是否跨用户扩散，发送飞书告警。',
    updated_at = NOW()
WHERE name = '真实用户客户端失败增多'
  AND metric_type = 'client_visible_failure_count';

