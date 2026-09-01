---
name: tokenkey-antigravity-fingerprint-alignment
description: >-
  Align TokenKey Antigravity cloudcode-pa fingerprint to locally installed agy CLI. Use when client-release-watch reports antigravity-cli drift or UA/body/header mimicry needs refresh; agy --version is the version owner.
---

# TokenKey：Antigravity CLI（`agy`）指纹对齐

Ground truth = **本机 `agy`**（`brew install --cask antigravity-cli`）。对齐靶 =
`backend/internal/pkg/antigravity/oauth.go` 的 `DefaultUserAgentVersion` 与
`BuildUserAgent`（`antigravity/cli/<ver> darwin/arm64`）。

**不再使用 Antigravity IDE**（`language_server`、spawn-validate、IDE mitm）作为指纹来源。

## 对齐靶

| 维度 | 路径 | 说明 |
|---|---|---|
| UA 版本 | `oauth.go` `DefaultUserAgentVersion` | 与 `agy --version` 一致 |
| UA 格式 | `oauth.go` `BuildUserAgent` | `antigravity/cli/%s darwin/arm64` |
| body userAgent | `request_transformer.go` | `antigravity` |
| ideType 等 | `client.go` | `ANTIGRAVITY` / `GEMINI` 等（wire 回归时再验） |
| gl-node | `client.go` | **不发**（#756） |

## 默认流程（版本 bump，无需 mitm）

```bash
brew install --cask antigravity-cli   # 或 brew upgrade --cask antigravity-cli
bash ops/antigravity/capture-antigravity-fingerprint.sh check env
bash ops/antigravity/capture-antigravity-fingerprint.sh check
bash ops/antigravity/capture-antigravity-fingerprint.sh emit-edits
# 按 emit-edits 更新 DefaultUserAgentVersion + oauth_test.go
python3 -m unittest discover -s ops/antigravity -p 'test_*.py' -t ops/antigravity
./scripts/preflight.sh
```

## 可选：wire 回归（body / ideType）

仅当怀疑非版本字段漂移时，用 `agy` + mitmproxy（**不是 IDE**）：

1. 将 mitm CA 加入 **login keychain**（Go 二进制不认 `NODE_EXTRA_CA_CERTS`）
2. `mitmdump` + 空目录内 `agy --print "pong"`（见历史 skill 2026-06-11 实测）
3. `bash ops/antigravity/capture-antigravity-fingerprint.sh capture --http`

## 漂移修复

- **版本**：`emit-edits` → bump `DefaultUserAgentVersion` + `oauth_test.go`
- **热推**：admin `antigravity_user_agent_version`（不发版）
- **changelog**：`docs/ops/antigravity-fingerprint-changelog.md` 追加一行

## 禁止

- 不以 IDE cask `antigravity` / `language_server` 作 ground truth
- 不从 release metadata alone bump（须 `agy --version` 或 wire bundle）
- 不捏造 JA3
