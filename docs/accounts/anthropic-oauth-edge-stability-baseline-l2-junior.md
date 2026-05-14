# Edge Anthropic OAuth 稳定基线（L2：初级程序员）

适用对象：有日常开发使用，自动化较少的人群。

## 硬约束（必须满足）

- 分级配置必须在**单 Anthropic OAuth 账号**安全范畴内执行。
- 任何承载流量的字段都不得超过该账号满配值的 70%。
- L2 流量系数固定为：`40% of full`。

流量字段集合：`rate_multiplier`、`concurrency`、`base_rpm`、`max_sessions`、`window_cost_limit`。

## 满配值口径与计算

- `full` 取同 edge Anthropic OAuth 稳定参考账号最近 7 天稳定区间 P95。
- L2 `level_factor=0.40`。
- 整数字段：`target=floor(full*0.40)`；`rate_multiplier=round(full*0.40,2)`。
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

## L2 参数基线（按满配比例）

| 字段 | 规则 |
|---|---|
| `rate_multiplier` | `<= full * 0.40` |
| `concurrency` | `<= floor(full * 0.40)` |
| `base_rpm` | `<= floor(full * 0.40)` |
| `max_sessions` | `<= floor(full * 0.40)` |
| `window_cost_limit` | `<= floor(full * 0.40)` |
| `priority` | 建议 20（低于 L3/L4） |
| `rpm_sticky_buffer` | 建议 3 |
| `window_cost_sticky_reserve` | 建议 3 |

## 运营建议阈值（账号外策略）

- 日额度建议：`<= full_daily_budget * 0.40`
- 告警阈值：额度 75%、连续失败 5 次
- 自动化级别：允许受控重试，禁止夜间批量

## 变更后复核

1. `operation=check` 看 `diff_count` 是否收敛。
2. 逐项校验流量字段是否满足 `<= full * 0.40`。
3. 确认不存在任何字段超过 `full * 0.70`。
