-- tk_061_remove_routing_capacity_rejection_alert.sql
--
-- Retire the old "无可用账号拒绝激增" routing_capacity_rejection_count P0.
--
-- The new user_visible_failure_count P0 seeded in tk_060 is the single
-- experience-first guardrail for real users seeing terminal provider/platform
-- failures. Keeping the narrower routing-capacity rule active would double-page
-- the same empty-pool/no-available-account incident: once as the user-visible
-- failure, and again as one root-cause-specific signal.
--
-- Remove the default rule instead of leaving a second Feishu entry point. Any
-- still-firing event is resolved first because the evaluator will no longer see
-- the rule after deletion. Silences are also cleaned so no orphaned UI state
-- remains. Historical ops_alert_events are intentionally preserved; rule_id has
-- no foreign key and event title/description keep the incident context.
--
-- Idempotent: once the rule is gone each statement matches 0 rows.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_events
   SET status = 'resolved',
       resolved_at = NOW()
 WHERE status = 'firing'
   AND rule_id IN (
       SELECT id
         FROM ops_alert_rules
        WHERE name = '无可用账号拒绝激增'
          AND metric_type = 'routing_capacity_rejection_count'
   );

DO $$
BEGIN
    IF to_regclass('public.ops_alert_silences') IS NOT NULL THEN
        DELETE FROM ops_alert_silences
         WHERE rule_id IN (
               SELECT id
                 FROM ops_alert_rules
                WHERE name = '无可用账号拒绝激增'
                  AND metric_type = 'routing_capacity_rejection_count'
           );
    END IF;
END $$;

DELETE FROM ops_alert_rules
 WHERE name = '无可用账号拒绝激增'
   AND metric_type = 'routing_capacity_rejection_count';
