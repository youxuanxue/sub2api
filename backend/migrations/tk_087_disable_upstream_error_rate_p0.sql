-- tk_087_disable_upstream_error_rate_p0.sql
--
-- Delete the 8% P1 "上游错误率偏高" rule on every node. It never sent Feishu
-- and is leftover early-warning next to the 20% rule.
--
-- Keep "上游错误率极高" (upstream_error_rate > 20%). Prod already pages real
-- user-visible terminal failures via user_visible_failure_count, so the
-- evaluator skips this rate rule on prod. Edges still evaluate it and page
-- Feishu as P1 (rule severity is flipped here; leftover P0 copies are also
-- demoted at runtime).
--
-- Remove the 8% default instead of leaving a second unused rate rule.
-- Firing events are resolved first; silences are cleaned. Historical
-- ops_alert_events stay; rule_id has no foreign key.
--
-- Idempotent: once the 8% row is gone each delete matches 0 rows; the 20%
-- UPDATE is a no-op after the first apply.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_events
   SET status = 'resolved',
       resolved_at = NOW()
 WHERE status = 'firing'
   AND rule_id IN (
       SELECT id
         FROM ops_alert_rules
        WHERE name = '上游错误率偏高'
          AND metric_type = 'upstream_error_rate'
   );

DO $$
BEGIN
    IF to_regclass('public.ops_alert_silences') IS NOT NULL THEN
        DELETE FROM ops_alert_silences
         WHERE rule_id IN (
               SELECT id
                 FROM ops_alert_rules
                WHERE name = '上游错误率偏高'
                  AND metric_type = 'upstream_error_rate'
           );
    END IF;
END $$;

DELETE FROM ops_alert_rules
 WHERE name = '上游错误率偏高'
   AND metric_type = 'upstream_error_rate';

UPDATE ops_alert_events
   SET severity = 'P1'
 WHERE status = 'firing'
   AND rule_id IN (
       SELECT id
         FROM ops_alert_rules
        WHERE name = '上游错误率极高'
          AND metric_type = 'upstream_error_rate'
   );

UPDATE ops_alert_rules
   SET enabled = true,
       severity = 'P1',
       description = '仅 edge 评估：上游错误率（provider 端非 429/529 限流错误）超过 20% 且持续 5 分钟触发，飞书 P1。prod 不评估；用户侧终态失败由「真实用户体验受损」(user_visible_failure_count) 飞书 P0 覆盖。',
       updated_at = NOW()
 WHERE name = '上游错误率极高'
   AND metric_type = 'upstream_error_rate';
