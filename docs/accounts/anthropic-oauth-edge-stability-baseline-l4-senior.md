# Edge Anthropic OAuth 稳定基线（L4：资深工程师）

适用对象：高频使用、自动化成熟、对吞吐有更高要求的人群。

## 硬约束（必须满足）

- 分级配置必须在**单 Anthropic OAuth 账号**安全范畴内执行。
- 任何承载流量的字段都不得超过该账号满配值的 70%。
- L4 流量系数固定为：`70% of full`（分级上限）。

流量字段集合：`rate_multiplier`、`concurrency`、`base_rpm`、`max_sessions`、`window_cost_limit`。

## 满配值口径与计算

- `full` 取同 edge Anthropic OAuth 稳定参考账号最近 7 天稳定区间 P95。
- L4 `level_factor=0.70`（分级上限）。
- 整数字段：`target=floor(full*0.70)`；`rate_multiplier=round(full*0.70,2)`。
- 超过 `full*0.70` 一律截断并禁止 apply。

## 固定字段（所有等级一致）

- `platform=anthropic`
- `type=oauth`
- `proxy_id=null`
- `channel_type=0`
- `auto_pause_on_expired=true`
- `custom_base_url_enabled=false`
- `custom_base_url` 为空或不存在
- `enable_tls_fingerprint=true`
- TLS profile 使用 `claude_cli_nodejs24_fixed`
- `intercept_warmup_requests=true`
- `temp_unschedulable_enabled=true`

## L4 参数基线（按满配比例）

| 字段 | 规则 |
|---|---|
| `rate_multiplier` | `<= full * 0.70` |
| `concurrency` | `<= floor(full * 0.70)` |
| `base_rpm` | `<= floor(full * 0.70)` |
| `max_sessions` | `<= floor(full * 0.70)` |
| `window_cost_limit` | `<= floor(full * 0.70)` |
| `priority` | 建议 80 |
| `rpm_sticky_buffer` | 建议 6 |
| `window_cost_sticky_reserve` | 建议 6 |

## 运营建议阈值（账号外策略）

- 日额度建议：`<= full_daily_budget * 0.70`
- 告警阈值：额度 85%、流量突增+错误率双告警
- 自动化级别：允许夜间任务与自愈动作，必须留审计日志

## 变更后复核

1. `operation=check` 看 `diff_count` 是否收敛。
2. 逐项校验流量字段是否满足 `<= full * 0.70`。
3. 任一字段超 70% 立即退回 `plan-apply` 重新收敛。
