-- tk_058_update_routing_capacity_alert_description.sql
--
-- Align the seeded routing_capacity_rejection_count alert description with the
-- tk_057 SLA scope change: empty-pool routing rejections (error_owner=platform)
-- now count in SLA/error_rate numerators; the dedicated count rule remains the
-- P0 signal that isolates error_phase='routing' storms.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

UPDATE ops_alert_rules
SET
  description = '当 5 分钟内"无可用账号"路由拒绝(空池快速失败 429 + 镜像 edge 下游容量拒绝,error_phase=routing)累计 ≥ 50 次且持续 3 分钟时触发。这类 error_owner=platform 的路由容量拒绝现计入 SLA/error_rate 分子,但仍不冷却任何账号;比率告警 alone 可能不足以区分"账号都健康但池被高负载瞬时抢空"的风暴,因此本专用计数规则仍是唯一能按 error_phase=routing 隔离空池拒绝激增的 P0 信号。需立即补号/扩容或核对镜像 edge 容量。',
  updated_at = NOW()
WHERE name = '无可用账号拒绝激增'
  AND metric_type = 'routing_capacity_rejection_count';
