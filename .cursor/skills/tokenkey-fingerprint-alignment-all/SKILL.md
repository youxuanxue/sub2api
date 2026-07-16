---
name: tokenkey-fingerprint-alignment-all
description: >-
  Run the combined TokenKey fingerprint refresh for Claude Code, Kiro, Antigravity, Codex, Gemini CLI, and Grok CLI. Enter after client-release-watch reports drift, or when capture-all finds actionable fingerprint drift; use platform-specific skills for single-client refreshes.
---

# TokenKey：全平台指纹对齐（umbrella）

## 两层入口（版本发现 → 指纹对齐）

1. **版本发现（Layer 1）** — `bash ops/fingerprint/client-release-watch.sh scan --plan`
   轮询 GitHub/npm/Homebrew 的客户端 release，对比 TokenKey pin。`--plan` **严格只读**：不写 state/report/cache，只报警、不 capture、不 bump。
   输出会指向应加载的 skill（本 umbrella 或单平台 skill）。
2. **指纹对齐（Layer 2，本 skill）** — 在 Cursor **加载本 skill 或单平台 skill 后**，跑
   `capture-all-fingerprints.sh` 做真实 capture/diff，再合一个 PR。

CI 里 `.github/workflows/client-fidelity-watch.yml` 每日顺序跑 Layer 1 release scan → prompt registry-gate → prod aggregate，并开 tracking issue；
人工/agent  remediation 从 Layer 1 的 skill 路由进入 Layer 2。

一次对齐**所有**客户端指纹，合一个 PR。四条引擎**机制不同必须独立**——cc 主动重定向到
自建 collector + cc0 MITM；kiro 被动 pcap（端点硬编码不可重定向）；antigravity 用 mitmproxy
抓 HTTP（承重的是 UA 版本/header/body，JA3 不承重）；codex **无抓包**——本机 codex CLI 自带
指纹，直接读已安装二进制（`codex --version` + native strings）对照 TK pin，所以无任何前置、永远
能跑，但门禁是 `check` 不是 `capture`（与前三条机制不同，故仍是独立引擎，只是并入本 umbrella 编排）。
本 skill 只统一**编排 + PR**。

关联：`ops/fingerprint/client-release-watch.sh`（Layer 1 版本发现，含 cc-stainless / gemini-cli / grok-cli / kiro-cli / codex-vscode 族）、
`tokenkey-cc-fingerprint-alignment`（cc + Stainless SDK 单平台）、`tokenkey-kiro-fingerprint-alignment`
（kiro IDE/CLI 单平台）、`tokenkey-antigravity-fingerprint-alignment`（antigravity 单平台）、
`tokenkey-codex-fingerprint-alignment`（codex CLI + VS Code 族；读本机 codex CLI、无 mitm）、
`tokenkey-gemini-fingerprint-alignment`（gemini-cli UA pin）、
`tokenkey-grok-fingerprint-alignment`（grok-cli OAuth pin）、
`docs/accounts/kiro-tls-fingerprint-alignment-design.md`、`docs/antigravity-fingerprint-changelog.md`。

## 流程

```bash
# 跑四条引擎（各自前置条件不变：cc 需 cc0 栈；kiro 需 sudo + 真实 Kiro IDE；
# antigravity 需 mitmproxy + 真实 Antigravity IDE 信任 mitm CA；codex 无前置，读本机 CLI）：
bash ops/fingerprint/capture-all-fingerprints.sh \
  --cc-arg --http \
  --kiro-arg --proxy-port --kiro-arg 7890 \
  --antigravity-arg --proxy-port --antigravity-arg 8080
#   → 末尾打印 combined coverage + drift report：
#     0=全部要求证据已观察且 aligned，1=drift，2=error/invalid evidence，3=incomplete/skipped
# 只跑部分引擎：--skip-cc / --skip-kiro / --skip-antigravity / --skip-codex
# codex 无前置、默认就跑；本机没装 codex 时用 --skip-codex。
```

## 漂移后 → 一个 PR

按报告里哪个平台漂移，分别刷新其产物，**合并到一个 PR**：
- cc 漂移：编辑 `*-mimicry-baselines.json` / `constants.go` / `tk_canonical_cc_oauth.json`
  （遵循 cc skill 的 TLS↔HTTP 分轨纪律，禁止从 UA 推断 ja3）。
- kiro 漂移：`python3 ops/kiro/capture_kiro_fingerprint.py emit-profile --bundle <b>`
  → 刷新 `deploy/aws/stage0/tk_canonical_kiro_ide.json`。
- antigravity 漂移：bump `internal/pkg/antigravity/oauth.go` 的 `DefaultUserAgentVersion`
  + `oauth_test.go` 断言 + `docs/antigravity-fingerprint-changelog.md` 一行（JA3 不参与）。
- codex 漂移：`bash ops/openai/capture-codex-fingerprint.sh emit-edits`（或带 `--version X.Y.Z`）
  bump 5 个 codex 版本 pin（UA / `version` header / 探测版本 / en-zh 占位符）；只动版本，
  非版本 pin（`originator=codex_cli_rs`、`OpenAI-Beta`）从不自动改。`preflight` 的 codex
  fingerprint pin consistency 兜「半截 bump」。

然后 `scripts/preflight.sh` 全绿 → 一个分支、一个 PR 覆盖各平台的产物变更。

最终结论必须逐平台使用 `aligned / drift / incomplete / skipped / error`。显式 `--skip-*` 或证据未观察时返回 3，禁止输出“所有指纹已对齐”。Claude Code 子报告还必须区分 `first_party_oauth` 与 3p/API-key cohort，并拒绝把 HTTP-only TLS stub 当作 JA3 证据。

## 边界

- 不合并采集机制（cc redirect vs kiro pcap vs antigravity mitm vs codex 读本机 CLI）；不捏造任一平台的 ja3。
- 四平台漂移节奏不同（cc 频繁、kiro / antigravity 罕见、codex 随 CLI 升级）；若某次只有一个平台漂移，
  用对应单平台 skill 即可，本 umbrella 用于「想一次性扫全平台 / 合一个 PR」的场景。
