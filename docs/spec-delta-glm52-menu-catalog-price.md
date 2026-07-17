# GLM 5.2 Group Menu Catalog Price Delta

## Background

`/pricing` 的分组目录展示价应当来自 public catalog 官方价。GLM 5.2 的
overlay/fallback 已按 BigModel 官方价源修正，但分组目录的 channel-served
row 仍可能优先读取 channel 里残留的旧价，导致用户看到旧的 input/output/cache
read 价格。

## Delta

MODIFIED:

- 分组目录中，channel 仍决定目标分组是否可服务模型。
- 当 public catalog 存在同名模型时，展示价改为 public catalog 官方价。
- 当 public catalog 不存在该模型时，保留原 channel-only/custom 模型的 channel
  价格展示兜底。

## Scenarios

- `glm-5.2` channel 中残留旧价格时，分组目录展示 BigModel catalog 官方价。
- account fallback 和 channel-served row 复用同一 catalog lookup 规则。
- 未进入 public catalog 的自定义 channel 模型继续展示 channel 配置价。

## Validation

- `go test -tags=unit ./internal/service`
- `./scripts/preflight.sh`
