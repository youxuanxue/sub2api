---
name: tokenkey-grok-fingerprint-alignment
description: >-
  Align TokenKey grok-cli OAuth relay fingerprint pins to upstream @xai-official/grok releases.
  Use when client-release-watch reports grok-cli drift or xAI OAuth relay UA rejection.
---

# TokenKey：Grok CLI 指纹对齐（release watch → bump pin → PR）

Layer 1：`bash ops/fingerprint/client-release-watch.sh scan --plan` 发现 `grok-cli` 漂移后加载本 skill。

## Pin 靶

| 信号 | 路径 |
|---|---|
| grok-cli semver watch pin | `backend/internal/pkg/xai/oauth.go` `DefaultGrokCLIVersion` |
| Responses 路径 UA | `backend/internal/service/openai_gateway_grok.go`（`sub2api-grok/*` 族，与 CLI semver 分轨） |

Upstream：`npm @xai-official/grok`。

## 流程（capture 脚本待建）

1. 确认本机 grok CLI 版本：`npm view @xai-official/grok version` 或已安装 CLI
2. bump `DefaultGrokCLIVersion` 与 Chat 路径透传的 `grok-cli/*` 相关常量（若观测到联动）
3. `go test -tags=unit ./internal/pkg/xai/...` → `scripts/preflight.sh`

**禁止**仅凭 npm release 改 pin；OAuth relay 仍须一次真实授权/转发探针验证后再合 PR。
