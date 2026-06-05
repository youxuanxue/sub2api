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
| 仅 UA/版本漂移修复 | 机械 | 编辑 baselines.json `cc_version` → `check-cc-version-sync.py --write`（自动改 7 份副本，§4.1）|
| beta 集合漂移修复位点 | 判断 + 清单 | 本 skill §4.2（需抓包证据）|
| merge 后是否立刻 sync-runtime | 判断 | HTTP drift PR merge 后**默认先 apply**（无发版）；compile default 跟下一班 release。前提：节点二进制含棘轮修复（见 §5 ⚠️，v1.7.72 及更早不含）——旧二进制节点会被 reconciler 一个 tick 回滚，只能等发版 |

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

> **双峰（bimodal）beta 不再被当成硬 mismatch。** `bundle-from-artifacts` 现在把每个 model 族的**全量** beta 分布写进 bundle 的 `http_variants`（不再 last-wins 取一条样本）。`diff` / `check` 对一个族的判定规则：
> - 单一 beta 集合 → 老逻辑 `OK` / `FAIL`。
> - 出现 ≥2 种 beta 集合且 baseline 命中其中之一 → `INVESTIGATE`（`needs_investigation`，**不**计入 `has_actionable_mismatch`，`check` 退 0）。报告里给出 `[Nx] <beta>` 计数分布 + #429 提示。
> - 出现 ≥2 种但 baseline 一个都不命中 → 仍是 `FAIL`（真漂移，需重抓重对齐）。
>
> 即：cc Haiku 的 A/B 灰度不会再让 `check` 因为抓到哪半边而忽红忽绿。要改 `HaikuBetaHeader` 仍需先刻画 A/B 差异（请求用途 / 工具存在性 / 服务端 gating），见 youxuanxue/sub2api#429。

## 3) 解读 diff 报告

| 字段 | mismatch 含义 | 动作 |
|---|---|---|
| `tls.ja3_*` | ClientHello 变了 | 更新 `tk_canonical_cc_oauth.json` → `manage-anthropic-config.py apply` |
| `canonical.user_agent_version` | compile default 落后 | `identity_service_tk_canonical_http.go` + admin setting |
| `mimic.cli_version` / mimic UA | mimic 路径落后 | `constants.go` + `identity_service.go` |
| `*.stainless_package_version` | 以实测为准 | mitm/collector |
| `betas.*` (`FAIL`) | token 集合或顺序错（且非双峰，或 baseline 一个变体都不命中）| `anthropic-http-mimicry-baselines.json` + `constants.go` + tests |
| `betas.*` (`INVESTIGATE`) | 该族 beta **双峰**，baseline 命中其一 → 非硬错（exit 0）| 先刻画 A/B 差异，再按 #429 决定 canonical；勿凭单样本对齐 |

## 4) 代码修复清单（HTTP-only 型）

### 4.1 仅 UA / 版本漂移（最常见的 cc patch bump）

单一真值源 + 守卫自动生成，**人手只碰 2 个文件**：

1. 编辑 `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` 的 `cc_version`（唯一手改源）。
2. 跑 `python3 scripts/sentinels/check-cc-version-sync.py --write` —— 自动重写全部 7 份副本：
   - 4 个 Go 编译默认值：`constants.go` 的 `CLICurrentVersion` + `DefaultHeaders["User-Agent"]`、
     `identity_service.go` 的 `defaultFingerprint.UserAgent`、
     `identity_service_tk_canonical_http.go` 的 `DefaultClaudeCodeUserAgentVersion`。
   - 2 个死快照：`ops/stage0/smoke_lib.sh`、`deploy/aws/stage0/tk_canonical_cc_oauth.json` 的 `observed.user_agent`。
   - 1 个 go:embed 镜像（load-bearing，reconciler 自愈目标）：`backend/internal/baseline/anthropic-http-mimicry-baselines.json`
     与 deploy 源 byte-identical 同步。
3. 写 `docs/spec-delta-cc-<patch>.md`（人工记录，含 comprehensive 的 beta 分布）。

> skill 总是跑 `--write` 并 **review 生成的 diff**（编译兜底 UA 值值得扫一眼）。
> `check-cc-version-sync.py`（check 模式）在 preflight / CI 兜底防漂移——手工漏跑 `--write` 会被拦。
> `test_capture_cc_fingerprint.py` 的版本断言已派生自 `cc_version`，无需手改。

### 4.2 beta 集合漂移（comprehensive 抓到稳定新 token，且非 A/B 灰度）

`--write` 只同步版本字符串，**不碰 beta 列表**。beta 真变了才手改，且必须有抓包证据（见 §6）：

- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json` 的 `sonnet_opus` / `haiku` 数组。
- `backend/internal/pkg/claude/constants.go` 的 beta 常量 + `HaikuBetaHeader` / `FullClaudeCode*MimicryBetas()`。
- claude 包对应单测。
- 若新增 load-bearing 面：`scripts/sentinels/gateway-tk.json`。
- `docs/spec-delta-cc-<patch>.md` 记录 token 集合与分布。

## 5) 验证与 PR（默认 open_pr=true）

```bash
python3 scripts/sentinels/check-cc-version-sync.py --selftest && python3 scripts/sentinels/check-cc-version-sync.py
go test -tags=unit ./internal/pkg/claude/... -run TestFullClaudeCode
python3 -m unittest discover -s ops/anthropic -p 'test_capture_cc_fingerprint.py' -t ops/anthropic
./scripts/preflight.sh
```

**HTTP 漂移（默认）：** 修 §4 清单（仅版本走 4.1 的 `--write`；beta 变了再走 4.2）→ spec-delta → 分支 → commit → push → `gh pr create` → **merge 后立刻**：

```bash
bash ops/anthropic/cc_fingerprint_apply_http_runtime.sh
```

无需为对齐 beta/UA 专门发版；`constants.go` / embedded baseline 在下一班 release 追上 compile default 即可。

> ⚠️ **热更新生效前提：节点二进制含 mimicry selfheal 单调棘轮修复（v1.7.72 及更早版本均不含）。** 旧版 reconciler 的
> `EnsureClaudeCodeMimicryBaseline` 是无方向覆写（`!= wantUA` 即改回 embedded 值），会在一个
> tick 内把 sync-runtime 写入的新 UA **回滚到旧版本**（2026-06-05 在 2.1.163→2.1.165 bump 实证：
> apply 9/9 成功、数小时后 8/9 节点被拉回）。棘轮版只把「旧于 embedded」的值拉上来，新值幸存。
> 若 fleet 还有旧版二进制节点：对那些节点 apply 是无效操作，唯一持久路径是发版（embedded
> baseline 随镜像更新后 reconciler 自动推平，连 apply 都不用跑）。check 的 `http_ua_drift` 在
> 「已合并未发版」窗口对旧二进制节点必然报 violation——这是真实状态，不是误报。

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
    → [仅UA/版本?] 编辑 baselines.json cc_version → check-cc-version-sync --write（自动改全部副本）→ spec-delta
    → [beta集合变?] baselines 数组 + constants betas + tests + spec-delta（§4.2，需抓包证据）
    → preflight → open PR (default) → merge
    → sync-runtime / cc_fingerprint_apply_http_runtime.sh（无发版）
    → [可选] 下一班 release 更新 compile default
```
