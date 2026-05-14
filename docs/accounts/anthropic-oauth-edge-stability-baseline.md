# Edge Anthropic OAuth 稳定配置基线（入口）

该文件已调整为分级基线入口。

## 全局硬约束

- 所有分级配置必须在**单 Anthropic OAuth 账号**安全范畴内。
- 任何流量字段不得超过满配值的 70%。

## 满配值（full）定义与取数口径

执行分级配置前，必须先确定 `full` 口径并完成取数：

- `full` 为同 edge、同平台、同类型的单账号满配参考值。
- 优先使用稳定名单账号的 7 天稳定区间 P95。
- 计算分级值后执行 70% 上限校验，超限不得 apply。

详细公式与字段映射见：
- `docs/accounts/anthropic-oauth-edge-stability-baseline-index.md`

## 分级文档

- L1（新人小白）：`docs/accounts/anthropic-oauth-edge-stability-baseline-l1-novice.md`
- L2（初级程序员）：`docs/accounts/anthropic-oauth-edge-stability-baseline-l2-junior.md`
- L3（中级工程师）：`docs/accounts/anthropic-oauth-edge-stability-baseline-l3-mid.md`
- L4（资深工程师）：`docs/accounts/anthropic-oauth-edge-stability-baseline-l4-senior.md`
- 总索引：`docs/accounts/anthropic-oauth-edge-stability-baseline-index.md`

## 使用方式

建议流程：`check` → `plan-apply` →（确认后）`apply` → `check` 复核。

命令与字段语义请统一参考：
- `docs/accounts/anthropic-oauth-edge-stability-baseline-index.md`
- `scripts/check-edge-anthropic-oauth-stability.py`
