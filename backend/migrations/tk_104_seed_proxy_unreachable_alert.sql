-- tk_104_seed_proxy_unreachable_alert.sql
--
-- Seed the default-on "代理出口不可达" (proxy_unreachable_count) P0 alert rule.
--
-- Why (prod 2026-10-17, jp-us5 / 52.68.182.0:1080):
--   When a SOCKS/HTTP egress proxy IP stops accepting TCP from prod, accounts
--   bound to that proxy stay schedulable and fail at socks-connect time. Most
--   requests recover via failover (recovered-200); when the failover budget is
--   exhausted users see 502 "Upstream service temporarily unavailable". Existing
--   experience/rate alerts either wait for user-facing volume or bury the dial
--   error under a generic upstream label — network ops cannot act on "which IP".
--
-- Metric semantics (backend/internal/service/ops_proxy_unreachable_tk.go):
--   Count of status=active proxies that still bind at least one non-deleted
--   account and fail a short TCP dial to host:port from this node. No SOCKS/HTTP
--   handshake — this is the IP:port reachability signal. Prod-only evaluation
--   (edges skip the rule; egress dials originate on prod).
--
-- Threshold: ≥ 1 sustained 2 minutes → P0. Two consecutive evaluator ticks
-- (default interval 60s) absorb a single transient dial blip; 10m cooldown
-- absorbs flap after the first page. notify_email=true routes it to Feishu P0.
--
-- Idempotent: ON CONFLICT (name) DO NOTHING.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

INSERT INTO ops_alert_rules (
    name, description, enabled, metric_type, operator, threshold,
    window_minutes, sustained_minutes, severity, notify_email, cooldown_minutes,
    created_at, updated_at
) VALUES (
    '代理出口不可达',
    '当任一仍绑定可调度账号的 active 代理（host:port）从 prod 节点连续 2 分钟 TCP 拨号失败时触发。用于出口 SOCKS/HTTP 代理宕机或网络阻断的即时 P0；飞书主因行给出代理名与 IP:port。不统计闲置或已全部下调度的代理。edge 节点运行时跳过该规则。',
    true, 'proxy_unreachable_count', '>=', 1.0, 1, 2, 'P0', true, 10, NOW(), NOW()
) ON CONFLICT (name) DO NOTHING;
