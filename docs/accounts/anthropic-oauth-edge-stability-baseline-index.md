# Anthropic OAuth Edge Stability Baseline Index

本索引把 Anthropic OAuth 账号稳定基线拆成按用户人群等级的多套文档。

## 全局硬约束

- 所有分级配置必须在**单 Anthropic OAuth 账号**安全范畴内。
- 任何流量字段不得超过满配值的 70%。
- 等级流量系数：L1=25%，L2=40%，L3=55%，L4=70%。

流量字段集合：`rate_multiplier`、`concurrency`、`base_rpm`、`max_sessions`、`window_cost_limit`。

## 满配值（full）定义与取数口径

### 1) full 的定义

- `full` 指同一 edge、同平台（`anthropic`）、同类型（`oauth`）单账号在稳定运行状态下的满配参考值。
- 取值来源优先顺序：
  1. 已纳入稳定名单的参考账号（`stable_accounts`）
  2. 人工确认的满配快照（经审批记录）

### 2) 推荐取数窗口

- 时间窗口：最近 7 天。
- 统计口径：使用稳定区间的 P95 作为 `full`，避免用瞬时峰值放大配置。

### 3) 字段映射

- `full_rate_multiplier`
- `full_concurrency`
- `full_base_rpm`
- `full_max_sessions`
- `full_window_cost_limit`
- `full_daily_budget`

### 4) 分级计算公式

- 整数字段（`concurrency/base_rpm/max_sessions/window_cost_limit`）：
  - `target = floor(full * level_factor)`
- 小数字段（`rate_multiplier`）：
  - `target = round(full * level_factor, 2)`
- 日额度：
  - `target_daily_budget = floor(full_daily_budget * level_factor)`

其中 `level_factor`：L1=0.25，L2=0.40，L3=0.55，L4=0.70。

### 5) 安全截断规则

- 若任意计算结果超过 `full * 0.70`，强制截断到 `full * 0.70`。
- 任一字段超上限时，不得 `apply`，必须回到 `plan-apply` 重新收敛。

## 分级基线

- L1（新人小白）：`anthropic-oauth-edge-stability-baseline-l1-novice.md`
- L2（初级程序员）：`anthropic-oauth-edge-stability-baseline-l2-junior.md`
- L3（中级工程师）：`anthropic-oauth-edge-stability-baseline-l3-mid.md`
- L4（资深工程师）：`anthropic-oauth-edge-stability-baseline-l4-senior.md`

## 统一流程

1. `operation=check`：只读检查 drift。
2. `operation=plan-apply`：生成待更新字段与 payload 预览。
3. 人工确认后 `operation=apply`：带固定确认口令执行更新。
4. 更新后再次 `check`：确认 `diff_count` 收敛。

## 参考

- 事实快照：`anthropic-oauth-edge-account-stability-2026-05-14.md`
- 机器可读基线：`deploy/aws/stage0/anthropic-oauth-stability-baseline.json`
- 巡检脚本：`scripts/check-edge-anthropic-oauth-stability.py`
