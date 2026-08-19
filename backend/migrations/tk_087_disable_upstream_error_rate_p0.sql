-- tk_087_disable_upstream_error_rate_p0.sql
--
-- Retire the Feishu P0 "上游错误率极高" (upstream_error_rate > 20%).
--
-- After tk_060/tk_064, prod already pages real user-visible provider/platform
-- terminal failures via user_visible_failure_count ("真实用户体验受损", ≥50 / 5m).
-- The 20% rate rule double-pages the same provider 5xx class on busy windows,
-- and on quiet windows it can still P0 on a handful of failures (rateSampleFloor
-- is 20 requests). That leftover P0 is from tk_014, before the UX count rule
-- existed.
--
-- Keep the 8% P1 "上游错误率偏高" rule as the single upstream-rate early warning.
-- It does not send Feishu (opsAlertFeishuSeverityAllowed only allows P0 plus
-- client_visible_failure_count).
--
-- Disable rather than delete: historical events keep their rule_id, and the
-- admin rule list shows the retired default. Idempotent: 0 rows on re-run.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_events
   SET status = 'resolved',
       resolved_at = NOW()
 WHERE status = 'firing'
   AND rule_id IN (
       SELECT id
         FROM ops_alert_rules
        WHERE name = '上游错误率极高'
          AND metric_type = 'upstream_error_rate'
   );

UPDATE ops_alert_rules
   SET enabled = false,
       description = '已停用：与 prod「真实用户体验受损」(user_visible_failure_count) 飞书 P0 重复。用户侧终态失败只由该计数规则分页；上游比例预警保留「上游错误率偏高」(8%, P1, 不发飞书)。',
       updated_at = NOW()
 WHERE name = '上游错误率极高'
   AND metric_type = 'upstream_error_rate'
   AND enabled = true;

UPDATE ops_alert_rules
   SET description = '上游错误率（provider 端非 429/529 限流错误，已排除客户端/网关侧失败与限流）超过 8% 且持续 5 分钟触发。仅作早期预警，不发飞书；用户侧事故由「真实用户体验受损」P0 覆盖。',
       updated_at = NOW()
 WHERE name = '上游错误率偏高'
   AND metric_type = 'upstream_error_rate';
