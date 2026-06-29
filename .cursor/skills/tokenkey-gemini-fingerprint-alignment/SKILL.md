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

## 流程（capture 脚本待建）

1. 安装/确认本机 `@google/gemini-cli` 版本：`npm view @google/gemini-cli version`
2. 对照 `internal/pkg/geminicli/constants.go` 中 `GeminiCLIUserAgent` 的 semver
3. bump UA 字面量 + 相关测试断言
4. `go test -tags=unit ./internal/pkg/geminicli/...` → `scripts/preflight.sh`

**禁止**仅凭 npm release 改 pin；至少对照本机 CLI `--version` 或一次真实 gateway 探针后再合 PR。
