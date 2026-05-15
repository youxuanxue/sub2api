# Anthropic OAuth Edge Stability Baseline Index

Anthropic OAuth 分级基线已改为 JSON 管理，不再使用 L1-L4 markdown 文档。

## 规则（简版）

- 分级配置仅允许在**单 Anthropic OAuth 账号**安全边界内执行。
- 任何流量相关字段不得超过满配的 70%。
- `rate_multiplier` 固定为 `1.0`（计费相关，不作为流量约束旋钮）。
- 满配 `full` 采用官方事实口径的预估值：以 Anthropic 文档中的 Tier 3（`$200` credit purchase threshold）为参考，不使用运行时窗口值。

## 基线文件

- 现有稳定基线（单套）：`deploy/aws/stage0/anthropic-oauth-stability-baseline.json`
- 分级基线（L1-L4）：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`

## 操作流程

1. 选择等级（L1/L2/L3/L4）并读取对应 JSON 基线。
2. 执行 `check` / `plan-apply`，确认字段与目标等级一致。
3. 执行 `apply` 前再次校验：不得超过 70% 上限。
4. `apply` 后复核 drift 收敛。
