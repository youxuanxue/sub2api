---
name: tokenkey-codex-fingerprint-alignment
description: >-
  Align the TokenKey OpenAI-platform Codex client fingerprint to the locally-installed Codex CLI. Use when `codex` upgrades and the forged UA / `version` header / probe version / admin-UI placeholders need a version bump, or to diff TK pins against the installed CLI. Reads the installed binary (no mitmproxy); never auto-changes the non-version pins (originator=codex_cli_rs, OpenAI-Beta), and keeps the OS/terminal UA segment verbatim.
---

# TokenKey：Codex 指纹对齐（读本机 codex CLI → diff 常量 → 改常量 → PR）

适用于本仓库（TokenKey fork of sub2api）。把 **本机安装的 Codex CLI** 当 ground truth，
**TK 的 OpenAI 平台常量** 当待对齐对象。OpenAI OAuth 转发链路在伪造 / 兜底 Codex 客户端
身份时用这些常量；codex CLI 升级后它们会漂移，需要 bump。

关联：`tokenkey-cc-fingerprint-alignment`（anthropic）、`tokenkey-antigravity-fingerprint-alignment`
（antigravity）、`tokenkey-kiro-fingerprint-alignment`（kiro）、`tokenkey-fingerprint-alignment-all`
（umbrella，目前编排 cc/kiro/antigravity 三引擎）。参考实现真值：`backend/internal/service/openai_gateway_service.go`、
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

## 对齐靶（diff 的待对齐对象，全部读源码字面量，无 baseline JSON）

| pin | 真值源 | 含义 |
|---|---|---|
| `ua_default`（UA，含 2 处版本）| `service/setting_service.go` `DefaultOpenAICodexUserAgent` | 强制 / 兜底 UA + 后台设置默认值 |
| `gateway_version` | `service/openai_gateway_service.go` `codexCLIVersion` | 上游 `version` 请求头 |
| `probe_version` | `service/account_usage_service.go` `openAICodexProbeVersion` | 用量探测 `Version` 请求头 |
| `placeholder_en` | `frontend/src/i18n/locales/en.ts` `openaiCodexUserAgentPlaceholder` | 后台 UA 输入框占位提示 |
| `placeholder_zh` | `frontend/src/i18n/locales/zh.ts` `openaiCodexUserAgentPlaceholder` | 同上（中文） |
| originator（非版本）| `openai_gateway_service.go` `resolveOpenAIUpstreamOriginator` | `codex_cli_rs`（只读确认，不 bump）|
| OpenAI-Beta（非版本）| `openai_gateway_service.go` | `responses=experimental`（只读确认，不 bump）|

> **5 个版本 pin 必须始终相等。** PR #1013 差点漏掉 en/zh 占位符——这就是为什么
> `scripts/preflight.sh` 有一道 `codex fingerprint pin consistency` 机械门禁（`check-consistency`，
> 见下）。它只校验**5 个 pin 彼此一致**，**不**和上游 codex 比较，所以 codex 发新版本时**不会**
> 误红 CI；只有半截 bump（改了部分 pin）才会失败。

## 工具（`ops/openai/`）

- `capture-codex-fingerprint.sh` — 编排（`check env` / `show-baseline` / `diff` / `check` /
  `check-consistency` / `emit-edits`）。
- `capture_codex_fingerprint.py` — 确定性引擎：读 5 个 pin + 2 个非版本钉死项、读本机
  `codex --version`、diff、生成 bump 编辑、退出码门禁。
- `test_capture_codex_fingerprint.py` — 单测（含对真实源码文件的 LiveRepoTests，正则漂移即红）。

退出码：`0` 对齐 / 一致，`1` 漂移或 pin 之间不一致，`2` 用法 / 环境错误（codex 未装）。

## 流程（codex 升级后）

```bash
# 0) 确认本机 codex CLI 已升到目标版本
codex --version                                              # e.g. codex-cli 0.143.0
bash ops/openai/capture-codex-fingerprint.sh check env

# 1) diff：本机 codex vs 5 个 TK pin（人读报告 + 建议 bump）
bash ops/openai/capture-codex-fingerprint.sh diff
#    全 ✓ = 已对齐，无需动作；有 ✗ = 漂移，继续

# 2) 拿到确定性编辑清单（UA 只换版本 token、保留 OS/终端段；bare pin 整值替换）
bash ops/openai/capture-codex-fingerprint.sh emit-edits          # 目标=本机 codex 版本
#    或显式：emit-edits --version 0.143.0  /  --json

# 3) 按清单逐文件改（5 个 pin 全改，缺一不可），然后重建内嵌前端清单（动了 en/zh）：
pnpm --dir frontend run build                                # 刷新 backend/internal/web/dist/frontend-source.json

# 4) 门禁：一致性 + 单测 + 全量 preflight
bash ops/openai/capture-codex-fingerprint.sh check-consistency   # 5 pin 互相一致 → rc 0
python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
./scripts/preflight.sh
```

## 漂移修复

- **仅版本漂移（最常见）**：`emit-edits` 给出的 5 处全改。三个 Go 常量 + 两个 i18n 占位符；
  动了 i18n 必须 `pnpm --dir frontend run build` 重建 `frontend-source.json`（否则
  `frontend release asset contract` 门禁红）。`request.go` / `request_test.go` 里的 `0.14x.y`
  是**故意混版本的格式示例 / 前缀匹配测试**，与具体版本无关，**不改**。
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

漂移修复后：分支 → commit（改的 3 个 service 文件均已钉在 `scripts/sentinels/gateway-tk.json`，
属 TK 自有行为；i18n 占位符 + 内嵌清单一并提交）→ `gh pr create`。门禁标记：纯版本字面量
bump 用 `upstream-touch-trivial`（无回滚风险、无符号增删）+ `sentinel-registry-reviewed`
（既有锚点不变，无需新增）。TK 改动走 **Squash and merge**（参 CLAUDE.md §5.y）。

## 边界 / 禁止

- 不抓任何网络流量（codex 指纹本地可读，mitm / pcap 是 cc/kiro/antigravity 的机制，不搬过来）。
- 不新增 baseline JSON（单一真值源 = 源码字面量）。
- 不自动 bump 非版本钉死项（originator / OpenAI-Beta），也不据 binary-strings 缺失判漂移。
- 不改 UA 的 OS / 终端段（参考环境，非承重）；不改 `request.go` 的格式示例版本。
- 不挂每日 hook（codex 版本漂移随你本机升级触发，按需跑即可；preflight 的
  `check-consistency` 已兜住「半截 bump」这一真正会回归的失败模式）。
