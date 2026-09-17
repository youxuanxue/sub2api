---
name: tokenkey-cc-fingerprint-alignment
description: >-
  Align TokenKey Claude Code fingerprint to locally installed claude CLI version and wire capture when needed. Use after cc version changes, client-release-watch drift, OAuth mimicry/beta/stainless drift, or TLS/geo-stego body drift.
---

# TokenKey：cc 指纹对齐（抓包 → diff → 修代码 → PR）

适用于本仓库（TokenKey fork of sub2api）。把 **真实 cc 流量** 当作 ground truth，**TokenKey 常量 + DB TLS profile** 当作待对齐对象。TLS 与 HTTP **分轨采集、分轨决策**——禁止从 UA 版本号推断 ja3 或 `X-Stainless-Package-Version`。

**指纹范畴（OAuth mimic 路径）**：不仅是 `User-Agent` 版本。完整出站指纹 = **TLS JA3** + **HTTP 头**（`User-Agent`、`anthropic-beta` 全集、`x-stainless-*`、`x-app`）+ **system 表面**（`x-anthropic-billing-header` 块、identity anchor、geo-stego 类）。ingress `usage_logs.user_agent`（如 `OpenAI/Python`）≠ 上游所见；用 `gateway.anthropic_oauth_mimic_egress` 或 `probe-oauth-mimicry-chain.sh` 验证出站。见 `docs/spec-delta/cc-oauth-mimicry-fingerprint-scope.md`。

**版本 ground truth** = 本机 **`claude` CLI**（`~/.local/bin/claude` 或 `claude update`）。`client-release-watch` 的 **仅版本漂移** 走静态对齐（类 Codex / Antigravity），**不需要** cc0-here / Claude Desktop / mitm。

## 默认流程（版本 bump，无需 cc0/mitm）

```bash
claude update   # 或 npm i -g @anthropic-ai/claude-code@<ver>
bash ops/anthropic/capture-cc-fingerprint.sh check env --static
bash ops/anthropic/capture-cc-fingerprint.sh check
bash ops/anthropic/capture-cc-fingerprint.sh emit-edits
# 按 emit-edits 更新 deploy/aws/stage0/anthropic-http-mimicry-baselines.json cc_version
python3 scripts/sentinels/check-cc-version-sync.py --write
python3 scripts/sentinels/check-cc-version-sync.py --quiet
python3 ops/anthropic/test_capture_cc_fingerprint.py StaticVersionTests
./scripts/preflight.sh
```

`check env`（无 `--static`）与 `capture --http` 仍用于 **TLS / beta / system / geo-stego** 漂移。

先机械分类证据 cohort，再比较对应 baseline：

```bash
python3 ops/anthropic/capture_cc_fingerprint.py classify-config \
  --config-dir "${TOKENKEY_CC_CAPTURE_CONFIG_DIR:-$HOME/.claude}" \
  --claude-bin "$HOME/.local/bin/claude"
```

- `first_party_oauth`：允许比较 OAuth beta。
- `third_party_token` / `first_party_non_oauth`：只验证 UA、Stainless、system 等通用 HTTP 表面；OAuth beta 必须为 `NOT_OBSERVED`，禁止据此改 OAuth baseline。
- TLS 只接受 collector 或 passive pcap；`baseline_stub_http_only` 必须为 `NOT_OBSERVED`。

关联：本机 `cc0-here` / `claude0-here` launcher（抓包环境）、`tokenkey-anthropic-oauth-config`
skill（ja3 变更时的 TLS profile apply）、
`docs/spec-delta/cc-oauth-mimicry-fingerprint-scope.md` + `docs/ops/cc-fingerprint-changelog.md`。

## 抓包 / 日更

TLS·HTTP·geo 抓包命令与 cohort 分类见 `ops/anthropic/capture-cc-fingerprint.sh` 与
`ops/anthropic/capture_cc_fingerprint.py`。需要时手动跑
`bash ops/anthropic/cc_fingerprint_daily_hook.sh`（sessionStart 自动 hook 已关停）。


## 3) 解读 diff 报告
| 字段 | mismatch 含义 | 动作 |
|---|---|---|
| `tls.ja3_*` | ClientHello 变了 | 更新 `tk_canonical_cc_oauth.json` → `manage-anthropic-config.py apply` |
| `canonical.user_agent_version` | compile default 落后 | `identity_service_tk_canonical_http.go` + admin setting |
| `mimic.cli_version` / mimic UA | mimic 路径落后 | `constants.go` + `identity_service.go` |
| `*.stainless_package_version` | 以实测为准 | mitm/collector |
| `betas.*` (`FAIL`) | token 集合或顺序错（且非双峰，或 baseline 一个变体都不命中）| `anthropic-http-mimicry-baselines.json` + `constants.go` + tests |
| `betas.*` (`INVESTIGATE`) | 该族 beta **双峰**，baseline 命中其一 → 非硬错（exit 0）| 先刻画 A/B 差异，再按 #429 决定 canonical；勿凭单样本对齐 |
| `system.identity_anchor` (`FAIL`) | 真实 CC system 块不命中任一 canonical 身份锚点 = banner 漂移（上游 403 风险，**actionable**）| 走 §4.5：改注册表 + Go 副本 + spec-delta |
| `system.identity_anchor` (`SKIP`) | 本次未抓到 system 块（仅 TLS 跑）| 跑 `capture --http` 再看 |
| `system.billing_prefix` (`INVESTIGATE`) | 未见 `x-anthropic-billing-header` 块 → 非硬错 | count_tokens / 子请求本就不带；仅当正常 `/v1/messages` 也缺才查 |
| `geo.messages.currentDate` (`FAIL`) | messages `<system-reminder>` 日期行非 US 形态 | `capture-cc-fingerprint.sh` / `cc_geo_stego_align.sh` + §4.4 + `gateway_request_tk_cc_geo_stego.go` |
| `geo.date_change.newDate` (`FAIL`) | attachment 日期仍为 `/` | 同上 |

## 4) 代码修复清单（HTTP-only 型）

仅版本 bump 走上方静态流程：只手改 `cc_version`，`--write` 同步并 review diff；不单独写版本 spec。仅 beta/system/TLS/geo 真漂移时读对应细则，必须有对应真实证据，禁止从 UA 推断其它指纹。 见 [操作细则](references/code-changes.md)。

## 5) 验证与 PR

按 `ops/anthropic/capture-cc-fingerprint.sh` / sentinel selftests / `./scripts/preflight.sh`；
HTTP 合并后 `bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh`；TLS 用
`cc_fingerprint_open_tls_drift_pr.sh`（worktree 隔离）。


## 6) 禁止事项

- 未抓包就改 beta / stainless
- 未抓包就改 system prompt 锚点 / 注入 banner（`cc-system-prompt.json` + Go 副本）；banner 两文件须字节一致
- 试图 byte 对齐 system prompt 全文（动态：cwd/git/date/env）——只对齐锚点
- 从旧 patch 推断 ja3
- 把 HTTP-only bundle 内的 baseline stub 当成新 JA3 证据
- 用 3p / API-key cohort 的 beta 修改 OAuth baseline
- ja3 变了却只改 HTTP 常量
- 用 `cc0-here` 直接做 HTTP mitm（应走 `http_capture_invoke.sh`）
- 跳过 comprehensive 直接开 PR（beta 分裂未验证）
- 未跑 geo capture / `cc_geo_stego_align.sh` 就扩展 geo normalize 规则（须用真实 `capture.jsonl` line 作测试 fixture；修复清单见 §4.4）
- 把 `# Environment` 段 TZ/proxy 字符串当作 TokenKey 应改写的隐写
