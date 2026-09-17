## 5. 故障速查

| 现象 | 处理 |
|---|---|
| `snapshot` 失败 / SSM 拒绝 | 校验 Lightsail instance 与 SSM managed instance / `edge-targets-lightsail.json` / OIDC 权限 |
| `plan` 退出码 1（`any_stale=true`） | 检查 `plan.json` 的 `tier_summaries[*].ordering[*].stale_reasons`。常见：`never_sampled`（账号新建未跑过任何请求）、`session_window_expired`（账号长期无流量、窗口已 reset 但还没新请求触发头部采样）。可接受 → apply 即可（stale 账号已排到队尾）；不接受 → 跑几个 warmup 请求让 utilization 被采样后再 plan |
| `plan` 退出码 2 + `account_count > MAX_PER_TIER_PER_EDGE` | 该 edge 该 tier 账号太多，offset 会越级。先用 tier baseline 流水线把部分账号挪到上下 tier，再回来跑本流水线 |
| `apply --confirm` 拒绝 | 必须精确 `yes-rebalance-anthropic-priority` |
| `apply` 中途 SSM 失败 | 看 `apply-report.json` 的 `results[*].ssm_command_id`；用 `aws ssm get-command-invocation` 查具体 stderr；修因后**重新 plan**（不要直接 apply 旧 plan，因为部分账号已写、剩下账号 ranking 可能也变了） |
| `verify` 报 drift | 多为：(a) apply 之后又跑了 tier baseline apply，把 priority 重置了 → 重新 plan + apply；(b) admin UI 手工改了 priority → 看 audit log；(c) apply 时一部分 step 失败但 verify 比对全部 actions |
| 想全局重排（跨 tier） | 不支持，也**不要**这样做：会破坏 stability tier 的调度语义 |
