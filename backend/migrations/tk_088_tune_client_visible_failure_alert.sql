-- Reduce non-actionable client-fault paging. Caller cancellations are excluded
-- from the numerator in buildUserVisibleFailureWhere; this migration lowers the
-- remaining client-validation signal to an operational P2 notification.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET severity = 'P2',
    threshold = 50.0,
    window_minutes = 5,
    sustained_minutes = 5,
    notify_email = false,
    description = 'prod 节点 5 分钟内真实用户 client-owned 终态失败累计 ≥ 50 且持续 5 分钟时记录为 P2 运营信号。排除客户端主动取消（499/context canceled）；用于聚合同类请求问题并检查是否跨用户扩散，默认不发邮件。',
    updated_at = NOW()
WHERE name = '真实用户客户端失败增多'
  AND metric_type = 'client_visible_failure_count';

