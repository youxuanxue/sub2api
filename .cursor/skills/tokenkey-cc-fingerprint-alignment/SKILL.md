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

## 自动化（每日 sessionStart）

项目已注册 Cursor hook（`.cursor/hooks.json` → `sessionStart`）：

- 每个 **UTC 日** 在 Agent 会话启动时后台跑一次（`ops/anthropic/cc_fingerprint_daily_hook.sh`）。
- 先 `check env`（cc0 gost/SOCKS + claude0-here；Desktop 未开仅 WARN，见 `--relax-desktop`）。
- 再 TLS `capture` + `check-tls`。
- 若 **ja3 与 TokenKey baseline 不一致**，自动 `docs/spec-delta-cc-tls-drift-*.md` + `gh pr create`（需本机 `gh auth`）。
- 日志：`.tls_list/cc-fingerprint-daily-hook.log`；漂移摘要：`.tls_list/cc-fingerprint-drift-alert.json`。
- 自动开 PR 时,**所有 git 操作在 `git worktree add` 出的临时 worktree 里完成**(`.tls_list/.drift-worktree-${stamp}-$$`),user 当前 checkout / 当前分支不受影响;cleanup trap 兜底。

### 控制 env vars

| env var | 默认 | 作用 |
|---|---|---|
| `TOKENKEY_CC_DAILY_FORCE=1` | — | 强制重跑(忽略今日 STATE_FILE 锁) |
| `TOKENKEY_CC_DAILY_STATE_DIR` | `~/.cache/tokenkey/` | 一日一锁文件位置;跨 worktree / 跨 sub2api clone 共享 |
| `TOKENKEY_CC_DAILY_RELAX_DESKTOP` | `1` | Claude.app 未开时只 WARN(daily hook 默认开,手动 `check env` 默认严格) |
| `TOKENKEY_CC_DAILY_SKIP_EGRESS` | `0` | 跳过 egress IP 校验 |
| `TOKENKEY_CC_DAILY_DRY_RUN=1` | — | 直接调 `cc_fingerprint_open_tls_drift_pr.sh <bundle>` 时,跑 worktree + commit 但**跳过 git push + gh pr create**;输出 `DRY_RUN: would push ...`。用于第一次部署 / 调试 hook 链是否通,而不真的开 PR |

仅 macOS + 已配置 `~/.config/cc0/env` 时执行;云端 Linux Agent 自动 skip。

### 端到端 dry-run(operator 第一次装 hook 时)

```bash
# 1) 准备一个保证 drift 的 bundle(随便伪造 ja3_hash)
cat > /tmp/dry-bundle.json <<'JSON'
{"schema_version":1,"cc_version":"2.1.152","tls":{"ja3_hash":"deadbeef","ja3_raw":"771"},"http":{}}
JSON

# 2) 跑全流程(创建 worktree、写 spec-delta、commit),但不 push / 不开 PR
TOKENKEY_CC_DAILY_DRY_RUN=1 bash ops/anthropic/cc_fingerprint_open_tls_drift_pr.sh /tmp/dry-bundle.json

# 期望:`DRY_RUN: would push branch ...` + worktree 自动清理 + exit 0
```

## 确定性基线（机械化 vs 真判断）

| 步骤 | 类型 | 承载 |
|---|---|---|
| cc0-here / claude0-here 代理栈就绪 | 机械 | `bash ops/anthropic/capture-cc-fingerprint.sh check env` |
| 读取 TokenKey baseline | 机械 | `python3 ops/anthropic/capture_cc_fingerprint.py show-baseline` |
| TLS collector 采集 ClientHello | 机械 | `bash ops/anthropic/capture-cc-fingerprint.sh capture` |
| HTTP mitm 采集 `/v1/messages` headers | 机械 | `bash ops/anthropic/capture-cc-fingerprint.sh capture --http` |
| 多请求 beta 一致性校验（haiku/sonnet/opus 各 N 次） | 机械 | `bash ops/anthropic/capture-http-comprehensive.sh` |
| bundle 组装 + diff + `--check` 门禁 | 机械 | `capture_cc_fingerprint.py` / `check-tls` |
| HTTP 漂移修复 + spec-delta PR | 机械 | 分支 + commit + `gh pr create`（见 §5） |
| 每日 TLS 漂移开 PR | 机械 | `ops/anthropic/cc_fingerprint_open_tls_drift_pr.sh` |
| Phase 0 ingress cohort / admin UA | 机械 | `ops/observability/run-probe.sh` + admin settings |
| ja3 变更 → TLS profile SQL apply | 机械 | `manage-anthropic-config.py plan/apply/verify` |
| HTTP beta 漂移 → runtime manifest apply | 机械 | `plan-http-mimicry-sync` + `sync-runtime` 或 `cc_fingerprint_apply_http_runtime.sh` |
| 代码修复位点 | 判断 + 清单 | 本 skill §4 |
| merge 后是否立刻 sync-runtime | 判断 | HTTP drift PR merge 后**默认先 apply**（无发版）；compile default 跟下一班 release |

## 调用参数

```text
/tokenkey-cc-fingerprint-alignment cc_version=<optional> [http=false] [phase0=true] [open_pr=false]
```

| 参数 | 默认 | 语义 |
|---|---|---|
| `cc_version` | `claude --version` | 目标 cc patch |
| `http` | **true** | 跑 mitm HTTP（需 gost + cc0 OAuth）；`http=false` 仅 TLS |
| `phase0` | false | 抓包前先跑 ingress/admin 只读侦察；`phase0=true` 启用 |
| `open_pr` | **true** | 漂移时修代码 + spec-delta + 开 PR；`open_pr=false` 仅 capture + diff |
| comprehensive | **true**（内建） | 每次完整跑法在 HTTP capture 后**必跑** `capture-http-comprehensive.sh`（排查 beta 灰度/分裂）；无单独 opt-out 参数 |

**默认完整链路（无参数调用）：** check env → capture `--http` → comprehensive beta 一致性 → diff/check → [有 drift] 修代码 + 测试 + preflight + 开 PR。

## 1) 环境检查（必须先过）

```bash
source ~/.config/cc0/env
cd "$REPO_ROOT"

# 严格：cc0 gost/SOCKS/egress + Claude Desktop 须由 claude0-here 拉起
bash ops/anthropic/capture-cc-fingerprint.sh check env

# 仅 CLI 采集 / 每日 hook：Desktop 未开只 WARN
bash ops/anthropic/capture-cc-fingerprint.sh check env --relax-desktop
```

| 组件 | 含义 |
|---|---|
| `cc0-here` | launcher 存在；**cc0.gost** + **cc0.socks** 在监听；egress IP = `CC0_EXPECT_EGRESS_IP` |
| `claude0-here` | launcher 存在；**Claude.app** 在跑且带 `--proxy-server` + `--disable-quic`（macOS） |

JSON：`python3 ops/anthropic/capture_cc_fingerprint.py check-env --json`

## 2) Ground truth 采集

### 2.1 环境

```bash
~/.local/bin/claude --version
~/.local/bin/cc0-here --version
```

TLS 打 collector **不需要** gost；HTTP mitm 打 `api.anthropic.com` 需 cc0 链（见 §HTTP 注意）。

### 2.2 一键采集 + diff（默认含 HTTP）

```bash
bash ops/anthropic/capture-cc-fingerprint.sh capture --http
# 仅 TLS：bash ops/anthropic/capture-cc-fingerprint.sh capture
```

### 2.3 门禁

```bash
# 全量 HTTP+TLS 关键字段（beta 缺 capture 为 SKIP）
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/…-cc-capture.bundle.json

# 仅 TLS ja3（每日 hook / 开 PR 用）
bash ops/anthropic/capture-cc-fingerprint.sh check-tls --bundle .tls_list/….bundle.json
```

### 2.4 HTTP mitm 链（已修复）

默认路径 **`ops/anthropic/http_capture_invoke.sh`**（`capture --http` 自动调用）：

```text
plain claude + CC0_USER_OVERLAY OAuth
  → mitm :11803 (log anthropic-beta)
  → gost :11800
  → SOCKS :1093
  → egress
```

- 在 **`/tmp`** 下发起请求，避免 sub2api 仓库 SessionStart 短路。
- 使用 `NODE_EXTRA_CA_CERTS` + `NODE_TLS_REJECT_UNAUTHORIZED=0`（**不走 cc0-here**，因 cc0 白名单不转发 CA）。
- 采集前 `check env` 会校验 gost 在 `CC0_GOST_HTTP_PORT` 监听。
- 覆盖 launcher：`CC0_HTTP_CLAUDE_BIN=/path/to/custom`（默认 `http_capture_invoke.sh`）。

### 2.5 多请求 beta 一致性校验（默认必跑）

`capture --http` 是**单次**抓包做 diff/check。完整 skill 跑法在单次 capture 之后**必须**再跑 comprehensive，跨 haiku/sonnet/opus 各 N 次并统计每族 beta 是否全一致（排查灰度 / 分裂）：

```bash
bash ops/anthropic/capture-http-comprehensive.sh
# 调整每族请求数：TOKENKEY_CC_CAPTURE_HAIKU_N / _SONNET_N / _OPUS_N（默认 3/3/2）
# 深查时重复多轮（例如 5 次）以确认跨 session 稳定
```

输出每个 model 族的 `N requests, M unique beta header(s)` + `OK/WARN`；末尾自动用最新 `tls-observed` bundle 跑一次 repo `diff` / `check`。复用 §2.4 同一条 mitm 链（gost + cc0 OAuth）。

任一 model 族出现 `WARN`（多种 beta）→ 在 PR / spec-delta 中记录分布，**禁止**在未抓包证据下改 beta 常量。

## 3) 解读 diff 报告

| 字段 | mismatch 含义 | 动作 |
|---|---|---|
| `tls.ja3_*` | ClientHello 变了 | 更新 `tk_canonical_cc_oauth.json` → `manage-anthropic-config.py apply` |
| `canonical.user_agent_version` | compile default 落后 | `identity_service_tk_canonical_http.go` + admin setting |
| `mimic.cli_version` / mimic UA | mimic 路径落后 | `constants.go` + `identity_service.go` |
| `*.stainless_package_version` | 以实测为准 | mitm/collector |
| `betas.*` | token 集合或顺序错 | `anthropic-http-mimicry-baselines.json` + `constants.go` + tests |

## 4) 代码修复清单（HTTP-only 型）

1. `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（runtime 真值源；与 capture 对齐）
2. `backend/internal/pkg/claude/constants.go`（compile default；下一班 release 追上）
3. `backend/internal/service/identity_service_tk_canonical_http.go`
4. `backend/internal/service/identity_service.go`
5. `backend/internal/pkg/claude/constants_test.go`
6. `scripts/sentinels/gateway-tk.json`
7. `ops/stage0/smoke_lib.sh`
8. `docs/spec-delta-cc-<patch>.md`

## 5) 验证与 PR（默认 open_pr=true）

```bash
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
./scripts/preflight.sh
```

**HTTP 漂移（默认）：** 修 §4 清单 → spec-delta → 分支 → commit → push → `gh pr create` → **merge 后立刻**：

```bash
bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh
```

无需为对齐 beta/UA 专门发版；`constants.go` 在下一班 release 追上 compile default 即可。

**TLS 漂移：** `bash ops/anthropic/cc_fingerprint_open_tls_drift_pr.sh .tls_list/…-cc-capture.bundle.json`（worktree 隔离，不影响当前 checkout）。

`open_pr=false` 时只跑到 capture + comprehensive + diff/check，不写代码、不开 PR。

## 6) 禁止事项

- 未抓包就改 beta / stainless
- 从旧 patch 推断 ja3
- ja3 变了却只改 HTTP 常量
- 用 `cc0-here` 直接做 HTTP mitm（应走 `http_capture_invoke.sh`）
- 跳过 comprehensive 直接开 PR（beta 分裂未验证）

## 7) 流程图

```text
check env → capture --http → comprehensive (beta consistency)
    → check / check-tls
    → [ja3变?] manage-anthropic-config apply + TLS drift PR
    → [HTTP drift?] baselines.json + constants + tests + spec-delta
    → preflight → open PR (default) → merge
    → sync-runtime / cc_fingerprint_apply_http_runtime.sh（无发版）
    → [可选] 下一班 release 更新 compile default
```
