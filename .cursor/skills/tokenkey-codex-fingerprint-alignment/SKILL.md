---
name: tokenkey-codex-fingerprint-alignment
description: >-
  Align the TokenKey OpenAI-platform Codex client fingerprint to the locally-installed Codex CLI. Use when `codex` upgrades and the forged UA / `version` header / probe version need a version bump, or to diff the TK version owner and derived aliases against the installed CLI. Reads the installed binary (no mitmproxy); updates only `DefaultOpenAICodexVersion`, never treats admin-UI examples as fingerprint pins, never auto-changes the non-version pins (originator=codex_cli_rs, OpenAI-Beta), and keeps the OS/terminal UA segment verbatim.
---

# TokenKey：Codex 指纹对齐（读本机 codex CLI → diff 常量 → 改常量 → PR）

适用于本仓库（TokenKey fork of sub2api）。把 **本机安装的 Codex CLI** 当 ground truth，
**TK 的 OpenAI 平台版本 owner 与派生 aliases** 当待对齐对象。OpenAI OAuth 转发链路在伪造 /
兜底 Codex 客户端身份时用这些值；codex CLI 升级后 owner 需要 bump，aliases 必须继续从它派生。

关联：`tokenkey-cc-fingerprint-alignment`（anthropic）、`tokenkey-antigravity-fingerprint-alignment`
（antigravity）、`tokenkey-kiro-fingerprint-alignment`（kiro）、`tokenkey-fingerprint-alignment-all`
（umbrella，编排 cc/kiro/antigravity/codex 四引擎；codex 以 `check` 门禁并入，单独刷新仍用本 skill）。参考实现真值：`backend/internal/service/openai_gateway_service.go`、
`internal/pkg/openai/request.go`（codex 客户端识别）。

## 为什么与 cc / kiro / antigravity 都不同（无需 mitm / pcap）

- cc 靠 `ANTHROPIC_BASE_URL` 重定向到自建 collector；kiro 被动 pcap 抓 JA3；antigravity 用
  mitmproxy 抓 cloudcode-pa 的 HTTP。三者都要**截获客户端真实流量**才拿得到指纹。
- **Codex 的指纹直接随 CLI 本地发行**：承重信号就是 codex 的**版本号**（嵌在 UA、`version`
  请求头、用量探测 `Version` 头里）。所以 ground truth = `codex --version` + native 二进制
  strings，**不需要抓任何网络流量**。这是最简单的一条引擎。
- **OS / 终端段不承重**：UA 里的 `Mac OS 26.3.1; arm64` / `iTerm.app/3.6.11` 是采集机器的
  **参考环境**，引擎只把 **codex 版本 token** 当对齐目标，bump 时其余字面量**原样保留**
  （除非你主动想刷新参考环境——那是手工判断，不是漂移）。
- **非版本钉死项只读不改**：`originator=codex_cli_rs`、`OpenAI-Beta: responses=experimental`
  是 TK 故意钉死的，引擎拿二进制 strings 做**正向确认**（best-effort），但**绝不**因为
  strings 里没搜到就判漂移——Rust 二进制可能在运行时拼接该值。真要变只有上游开始 400 拒
  伪造请求时才查（见下「非版本漂移」）。

## 对齐靶（一个可编辑 owner + 派生 aliases，无 baseline JSON）

| target | 真值源 | 含义 |
|---|---|---|
| `version_source` | `service/setting_gateway_runtime.go` `DefaultOpenAICodexVersion` | 唯一可编辑版本 owner |
| `ua_default` | 同文件 `DefaultOpenAICodexUserAgent` | 从 owner 派生的强制 / 兜底 UA 与后台设置默认值 |
| `gateway_version` | 同文件 `codexCLIVersion` | 从 owner 派生的上游 `version` 请求头 |
| `probe_version` | 同文件 `openAICodexProbeVersion` | 从 owner 派生的用量探测 `Version` 请求头 |
| originator（非版本）| `openai_gateway_scheduling.go` `resolveOpenAIUpstreamOriginator` | `codex_cli_rs`（只读确认，不 bump）|
| OpenAI-Beta（非版本）| `openai_gateway_forward.go` / `openai_gateway_passthrough.go` | `responses=experimental`（只读确认，不 bump）|

`frontend/src/i18n/locales/{en,zh}/admin/settings.ts` 的 `openaiCodexUserAgentPlaceholder` 是 UI
格式示例，可以故意展示任意合法版本；它们不参与 fingerprint alignment，也不随 owner bump。

`scripts/preflight.sh` 的 `codex fingerprint pin consistency`（`check-consistency`）机械验证 UA、
gateway version 与 probe version 仍从 `DefaultOpenAICodexVersion` 派生。它不和移动中的上游版本
比较，因此 Codex 发布新版本不会误红 CI；派生链被改回独立字面量时才会失败。

## 工具（`ops/openai/`）

- `capture-codex-fingerprint.sh` — 编排（`check env` / `show-baseline` / `diff` / `check` /
  `check-consistency` / `emit-edits`）。
- `capture_codex_fingerprint.py` — 确定性引擎：读版本 owner、派生 aliases 与非版本钉死项，
  再读本机 `codex --version`、diff、生成 owner bump 编辑并提供退出码门禁。
- `test_capture_codex_fingerprint.py` — 单测（含对真实源码文件的 LiveRepoTests，正则漂移即红）。

退出码：`0` 对齐 / 一致，`1` 漂移或派生契约不一致，`2` 用法 / 环境错误（codex 未装）。

## 流程（codex 升级后）

```bash
# 0) 确认本机 codex CLI 已升到目标版本
codex --version                                              # e.g. codex-cli 0.143.0
bash ops/openai/capture-codex-fingerprint.sh check env

# 1) diff：本机 codex vs TK owner / aliases（人读报告 + 建议 bump）
bash ops/openai/capture-codex-fingerprint.sh diff
#    全 ✓ = 已对齐，无需动作；有 ✗ = 漂移，继续

# 2) 拿到确定性编辑清单（正常情况下只改版本 owner）
bash ops/openai/capture-codex-fingerprint.sh emit-edits          # 目标=本机 codex 版本
#    或显式：emit-edits --version 0.143.0  /  --json

# 3) 按清单更新 DefaultOpenAICodexVersion；不要改前端 placeholder

# 4) 门禁：一致性 + 单测 + 全量 preflight
bash ops/openai/capture-codex-fingerprint.sh check-consistency   # owner / aliases 契约一致 → rc 0
python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
./scripts/preflight.sh
```

## 漂移修复

- **仅版本漂移（最常见）**：按 `emit-edits` 更新 `DefaultOpenAICodexVersion`；UA、gateway version
  与 probe version 自动继承。i18n placeholders 以及 `request.go` / `request_test.go` 中的版本号
  都是格式示例 / 前缀匹配测试，与当前版本无关，**不改**。
- **OS / 终端段刷新（少见，可选）**：只有你想把参考环境换到新机器时才改 UA 的
  `(Mac OS …; arch) <terminal>/<ver>` 段；这是手工判断，不是漂移，`emit-edits` 不碰它。
- **非版本漂移（罕见，需人判断，不自动 bump）**：只有当上游开始对伪造请求返回 4xx，或
  `diff` 的非版本行显示 originator / beta 在新 codex 里**确实换了值**时，才动
  `resolveOpenAIUpstreamOriginator` / `OpenAI-Beta` 常量。binary-strings「not found」是
  **不确定**信号（Rust 可能运行时拼接），**不是**漂移证据——不要据此改钉死项。

## 验证 / PR

```bash
python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
./scripts/preflight.sh
```

漂移修复后：分支 → commit（`DefaultOpenAICodexVersion` owner 与派生契约由 Codex consistency
preflight 守卫）→ `gh pr create`。门禁标记：纯版本字面量 bump 用 `upstream-touch-trivial`
（无回滚风险、无符号增删）+ `sentinel-registry-reviewed`（既有锚点不变，无需新增）。TK 改动走
**Squash and merge**（参 CLAUDE.md §5.y）。

## 边界 / 禁止

- 不抓任何网络流量（codex 指纹本地可读，mitm / pcap 是 cc/kiro/antigravity 的机制，不搬过来）。
- 不新增 baseline JSON（单一真值源 = 源码字面量）。
- 不自动 bump 非版本钉死项（originator / OpenAI-Beta），也不据 binary-strings 缺失判漂移。
- 不改 UA 的 OS / 终端段（参考环境，非承重）；不改 i18n placeholder 或 `request.go` 的格式示例版本。
- 不挂每日 hook（codex 版本漂移随你本机升级触发，按需跑即可；preflight 的
  `check-consistency` 已兜住「半截 bump」这一真正会回归的失败模式）。
