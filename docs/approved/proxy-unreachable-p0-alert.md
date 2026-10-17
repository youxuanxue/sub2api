---
title: Proxy unreachable P0 alert
status: approved
approved_by: "feng (conversation approval 2026-10-17)"
approved_at: 2026-10-17
created: 2026-10-17
owners: [tk-platform]
scope: "ops_alert_rules seed + prod TCP probe for egress proxy IP:port reachability"
---

# 代理出口不可达 P0 告警

## Decision

在 prod ops alert evaluator 中增加默认开启的 P0 规则 **代理出口不可达**
（`metric_type=proxy_unreachable_count`）：

- 对 `status=active` 且仍绑定 **status=active 且 schedulable=true** 账号的代理，
  从本节点对 `host:port` 做短 TCP dial（不握手；dial 超时独立于 evaluator 总预算）。
- 任一不可达 → 计数 ≥1 → P0（**持续 2 分钟** / 连续两个评估周期，冷却 10 分钟，飞书）。
- 飞书主因行给出代理名与 `IP:port` 及 dial 错误，便于网络/运维对号。
- **仅 prod 评估**；edge 跳过（现网出口由 prod 拨号）。
- 不统计闲置代理；账号全部下调度后停止对该出口拨号/告警。

## Why

prod 2026-10-17：`jp-us5` / `52.68.182.0:1080` SOCKS 出口宕机时，绑定账号仍可调度，
请求在 socks-connect 失败；failover 多数 recovered-200，耗尽后用户侧 502。既有
体验/比率告警要等流量砸上，且文案收成通用上游故障，网络侧无法立刻定位出口 IP。

## Owners

| 项 | 路径 |
| --- | --- |
| 探测与主因 | `backend/internal/service/ops_proxy_unreachable_tk.go` |
| Evaluator / prod-only | `ops_alert_evaluator_service.go`、`ops_alert_notifications_tk_feishu.go` |
| 种子规则 | `backend/migrations/tk_104_seed_proxy_unreachable_alert.sql` |

## Non-goals

- 不自动下调度或改绑代理；告警只负责发现与命名。
- 不做 SOCKS/HTTP 认证握手探测（只验 IP:port 可达）。
- 不改变账号调度、failover 或用户可见状态码语义。
