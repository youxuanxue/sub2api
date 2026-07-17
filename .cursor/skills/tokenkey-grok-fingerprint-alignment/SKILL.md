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

## 流程

1. 升级本机 CLI：`npm i -g @xai-official/grok@<target>`（**不是** npm 上的 `grok-cli` 包）
2. `bash ops/xai/capture-grok-fingerprint.sh check env`
3. `bash ops/xai/capture-grok-fingerprint.sh capture`（写入 `.cache/fingerprint/grok-cli/*.bundle.json`）
4. 有 drift 时 bump `DefaultGrokCLIVersion` + `go test -tags=unit ./internal/pkg/xai/...` → `scripts/preflight.sh`

**禁止**仅凭 npm release 改 pin；至少对照本机 `grok --version`（capture 脚本）后再合 PR。
