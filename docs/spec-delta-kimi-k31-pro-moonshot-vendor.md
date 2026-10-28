# kimi-k3.1-pro 公开厂商

## Background

`kimi-k3.1-pro` 由火山 Agent Plan 供货，公开定价卡片却标成 VolcEngine。价格表把供货渠道写进了 `litellm_provider`。公开厂商应跟原厂，和已经按 Moonshot 展示的 `kimi-k3`、按 MiniMax 展示的 `minimax-m3` 同一规则。

## Delta

- MODIFIED: `kimi-k3.1-pro` 的 `litellm_provider` 从 `volcengine` 改为 `moonshot`。公开目录的 vendor 徽章因此显示 Moonshot。
- 单价不变：输入 $1.94 / 1M、输出 $10.149 / 1M。调度仍走火山 Agent Plan 账号映射，不新增 Moonshot 渠道。
- 前端不改代码。现有目录卡片按 catalog `vendor` 取图标和文案，`moonshot` 已映射为 Moonshot。

## Scenarios

- 正向：公开目录里 `kimi-k3.1-pro` 的 vendor 是 `moonshot`，卡片显示 Moonshot，价格与改前相同。
- 负向：该模型不改挂到 Moonshot 官方渠道；火山 Plan 映射键保持不变。
- 回归：`kimi-k3` 仍是 `moonshot`，豆包原厂模型仍是 `volcengine`。

## Validation

- `go test -tags=unit -count=1 -timeout 180s ./internal/service/ -run TestPublicCatalog_PlanSuppliedModelsKeepOEMVendor` 通过。
- `./scripts/preflight.sh` 通过。
