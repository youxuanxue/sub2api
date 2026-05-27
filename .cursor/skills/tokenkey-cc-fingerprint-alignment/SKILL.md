---
name: tokenkey-cc-fingerprint-alignment
description: >-
  End-to-end workflow to capture real Claude Code (cc0-here / claude0-here) TLS
  and HTTP fingerprints, diff against TokenKey repo constants
  (tk_canonical_cc_oauth, constants.go, identity_service*), fix drift, and open
  a spec-delta PR. Use when cc patch releases, ingress UA cohort mixes, OAuth
  mimicry/beta/stainless drift is suspected, or after PR #423-style alignment
  needs repeating for a new cc version.
---

# TokenKey：cc 指纹对齐（抓包 → diff → 修代码 → PR）

适用于本仓库（TokenKey fork of sub2api）。把 **真实 cc 流量** 当作 ground truth，**TokenKey 常量 + DB TLS profile** 当作待对齐对象。TLS 与 HTTP **分轨采集、分轨决策**——禁止从 UA 版本号推断 ja3 或 `X-Stainless-Package-Version`。

关联：`cc0-claude0-launcher` skill（cc0-here 环境）、`tokenkey-anthropic-oauth-config` skill（ja3 变更时的 TLS profile apply）、`docs/spec-delta-cc-canonical-ua-beta-2.1.152.md`（PR #423 实例）。

## 确定性基线（机械化 vs 真判断）

| 步骤 | 类型 | 承载 |
|---|---|---|
| 读取 TokenKey baseline（ja3、UA 版本、stainless、mimicry beta 列表） | 机械 | `python3 ops/anthropic/capture_cc_fingerprint.py show-baseline` |
| TLS collector 采集 ClientHello | 机械 | `bash ops/anthropic/capture-cc-fingerprint.sh capture` |
| HTTP mitm 采集 `/v1/messages` headers | 机械 | `bash ops/anthropic/capture-cc-fingerprint.sh capture --http` |
| bundle 组装 + diff + `--check` 门禁 | 机械 | `capture_cc_fingerprint.py bundle-from-artifacts` / `diff` / `check` |
| Phase 0 ingress cohort / admin UA | 机械 | `ops/observability/probe-us1-cc-ua-setting.sh` 等（可选） |
| ja3 变更 → TLS profile SQL apply | 机械 | `ops/anthropic/manage-anthropic-config.py plan/apply/verify` |
| 代码修复位点（constants / identity / gateway） | 判断 + 清单 | 本 skill §4 |
| 是否发版 / admin PATCH 先后 | 判断 | `tokenkey-stage0-release-rollout` skill |
| PR 风险分级 / spec-delta 是否足够 | 判断 | `product-dev.mdc` |

## 调用参数

```text
/tokenkey-cc-fingerprint-alignment cc_version=<optional> [http=true] [phase0=true] [open_pr=false]
```

| 参数 | 语义 |
|---|---|
| `cc_version` | 目标 cc patch；缺省从 `claude --version` 读取 |
| `http=true` | 同时跑 mitm HTTP（需 gost + cc0 OAuth） |
| `phase0=true` | 抓包前先跑 ingress/admin 只读侦察 |
| `open_pr=false` | 默认只 capture + diff + 修复建议；显式 true 才开分支提交 |

## 0) 触发条件

- cc 新 patch（约每 2–4 天）
- 同 OAuth 账号 `usage_logs.user_agent` 混多个 patch
- `extra usage` / third-party 怀疑指纹而非并发
- 上次对齐后 cc 升级

## 1) Phase 0（只读，可选）

区分 **ingress**（客户端进来）与 **upstream**（TokenKey 发出）：

1. 各 edge `claude_code_user_agent_version` admin setting
2. canonical 账号 ingress UA cohort（120m 窗）
3. 编译默认：`DefaultClaudeCodeUserAgentVersion`、`CLICurrentVersion`

**不在 Phase 0 改代码。**

## 2) Ground truth 采集

### 2.1 环境

```bash
source ~/.config/cc0/env   # CC0_GOST_HTTP_PORT, CC0_USER_OVERLAY, …
~/.local/bin/claude --version   # TLS 采集默认用这个
cc0-here --version              # HTTP mitm 路径用这个（需 gost + OAuth）
```

TLS 打 collector **不需要** gost；HTTP mitm 打 `api.anthropic.com` **必须** cc0-here（或 `CC0_HTTP_CLAUDE_BIN`）。

Desktop 形状不同则 **`claude0-here`** 另抓一条 HTTP（本脚本默认 CLI）。

### 2.2 一键采集 + diff

```bash
cd "$REPO_ROOT"

# TLS only（最低门槛；ja3 + collector 侧 stainless/UA）
bash ops/anthropic/capture-cc-fingerprint.sh capture

# TLS + HTTP（mitm：anthropic-beta 顺序 + X-Stainless-*）
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
```

产出（默认 `$REPO_ROOT/.tls_list/`，gitignore）：

- `*cc-capture.tls-observed.json` — collector `fingerprints[0]`
- `*cc-capture.bundle.json` — diff 输入
- 终端打印 `capture_cc_fingerprint.py diff` 报告

### 2.3 仅 diff 已有 bundle

```bash
python3 ops/anthropic/capture_cc_fingerprint.py diff --bundle .tls_list/YYYYMMDD…-cc-capture.bundle.json
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/….bundle.json
# exit 1 = 有关键 mismatch，需修代码或 TLS profile
```

### 2.4 手工组装 bundle（已有 hook 产物）

```bash
python3 ops/anthropic/capture_cc_fingerprint.py bundle-from-artifacts \
  --tls-json .tls_list/20260527T….capture.json \
  --http-log /tmp/http.log \
  --cc-version 2.1.152 \
  --out .tls_list/manual.bundle.json
```

## 3) 解读 diff 报告

| 字段 | mismatch 含义 | 动作 |
|---|---|---|
| `tls.ja3_*` | ClientHello 变了 | 更新 `deploy/aws/stage0/tk_canonical_cc_oauth.json` → `manage-anthropic-config.py apply` |
| `canonical.user_agent_version` | compile default 落后 | `identity_service_tk_canonical_http.go` + admin setting |
| `mimic.cli_version` / mimic UA | mimic 路径落后 | `constants.go` `CLICurrentVersion` + `DefaultHeaders` + `identity_service.go` |
| `*.stainless_package_version` | **不能从 UA 推断** | 以 mitm/collector 实测为准（#423：0.94.0） |
| `betas.sonnet_mimicry` / `haiku_mimicry` | token 集合或**顺序**错 | `FullClaudeCode*MimicryBetas()` + `constants_test.go` |

**ja3 相同 → 不改 DB cipher/extension 体**；只更新 profile description 里的 cc 版本说明即可。

## 4) 代码修复清单（HTTP-only 型，PR #423 型）

1. `backend/internal/pkg/claude/constants.go` — beta、`DefaultHeaders`、`CLICurrentVersion`
2. `backend/internal/service/identity_service_tk_canonical_http.go` — `DefaultClaudeCodeUserAgentVersion`、`canonicalHTTPObservedStatic`
3. `backend/internal/service/identity_service.go` — mimic `defaultFingerprint`
4. `backend/internal/service/gateway_service.go` — Haiku/Sonnet mimic beta；**注释必须与抓包一致**
5. `backend/internal/pkg/claude/constants_test.go` — 抓包顺序回归
6. `scripts/sentinels/gateway-tk.json` — beta registry 锚点
7. `ops/stage0/smoke_lib.sh` — smoke UA 默认
8. `docs/spec-delta-cc-<patch>.md` — Evidence 段写 ja3/stainless/beta 实测值

## 5) 验证与 PR

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
go test -tags=unit ./internal/service/... -run 'TestGatewayService_getBetaHeader|TestNormalizeClaudeOAuthRequestBody_Haiku'
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
./scripts/preflight.sh
```

分支：`feature/cc-canonical-ua-beta-<patch>`

PR body：`Summary` / `Risk`（常规 HTTP 指纹；ja3 未变则无 migration）/ `Validation`（含 bundle path 或 Evidence 数值）

合并后：

1. Admin PATCH `claude_code_user_agent_version=<patch>`（canonical，无需 redeploy）
2. Release + deploy（mimic compile default）
3. Sonnet + Haiku smoke；24h `extra usage` 错误预算

## 6) 禁止事项

- 未抓包就改 beta 列表或 stainless 版本
- 假设「Haiku 不需要 claude-code beta」（必须以 mitm 为准）
- 从 2.1.142 ja3 推断 2.1.152 ja3（必须 2.1.Z 实测）
- ja3 变了却只改 HTTP 常量
- 注释与 `FullClaudeCodeHaikuMimicryBetas()` 矛盾

## 7) 流程图

```text
Phase0(可选) → capture [--http] → bundle.json
    → capture_cc_fingerprint.py check
    → [ja3变?] manage-anthropic-config apply
    → [HTTP drift?] constants + identity + gateway + tests
    → spec-delta → preflight → PR → merge → admin UA → deploy → smoke
```
