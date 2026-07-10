-- tk_064_raise_user_visible_failure_threshold.sql
--
-- Raise the prod P0 real-user experience pager from 20 failures / 5m to
-- 50 failures / 5m. This keeps the rule focused on clear user-visible incident
-- volume while preserving the same provider/platform-owned failure scope.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET
  threshold = 50.0,
  window_minutes = 5,
  description = 'prod 节点 5 分钟内真实用户可感知的 provider/platform 终态失败累计 ≥ 50 时触发。统计 status_code>=400、error_owner IN (provider,platform)、is_count_tokens=false 且可归属真实用户的请求；不包含 recovered-200。edge 节点运行时跳过该规则，避免和 prod 聚合线重复。该规则是用户体验兜底，根因由飞书卡片 breakdown 展示。',
  updated_at = NOW()
WHERE name = '真实用户体验受损'
  AND metric_type = 'user_visible_failure_count';
