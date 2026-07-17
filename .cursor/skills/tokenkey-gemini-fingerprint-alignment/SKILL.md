---
name: tokenkey-gemini-fingerprint-alignment
description: >-
  Align TokenKey Gemini CLI gateway fingerprint pins to upstream @google/gemini-cli releases.
  Use when client-release-watch reports gemini-cli drift or GeminiCLI/* User-Agent rejection.
---

# TokenKey：Gemini CLI 指纹对齐（release watch → bump pin → PR）

Layer 1：`bash ops/fingerprint/client-release-watch.sh scan --plan` 发现 `gemini-cli` 漂移后加载本 skill。

## Pin 靶

| 信号 | 路径 |
|---|---|
| `GeminiCLI/x.y.z` UA | `backend/internal/pkg/geminicli/constants.go` `GeminiCLIUserAgent` |

Upstream：`npm @google/gemini-cli`。

## 流程

1. 升级本机 CLI 到与 pin 相同或更新版本：`npm i -g @google/gemini-cli@<target>`
2. `bash ops/geminicli/capture-gemini-fingerprint.sh check env`
3. `bash ops/geminicli/capture-gemini-fingerprint.sh capture`（写入 `.cache/fingerprint/gemini-cli/*.bundle.json`）
4. 有 drift 时 bump `GeminiCLIUserAgent` + `go test -tags=unit ./internal/pkg/geminicli/...` → `scripts/preflight.sh`

**禁止**仅凭 npm release 改 pin；至少对照本机 `gemini --version`（capture 脚本）后再合 PR。
