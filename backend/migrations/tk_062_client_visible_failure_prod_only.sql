-- tk_062_client_visible_failure_prod_only.sql
--
-- Align the P1 client_visible_failure_count rule with the P0 user-visible rule:
-- prod is the user-facing aggregation point; edge mirror-relay traffic (e.g.
-- admin@api-us6 hitting edge-local accounts) must not page Feishu. Edge nodes
-- skip evaluation at runtime and suppress notifications as a defensive backstop.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET
  description = 'prod 节点 5 分钟内真实用户可感知的 client-owned 终态失败累计 ≥ 20 时触发。统计 status_code>=400、error_owner=client、is_count_tokens=false 且可归属真实用户的请求；用于运营同步客户参数、内容、权限、限额或用法问题。edge 节点运行时跳过该规则，避免 edge 本地探测/直连流量与 prod 聚合线重复告警。',
  updated_at = NOW()
WHERE name = '真实用户客户端失败增多'
  AND metric_type = 'client_visible_failure_count';
