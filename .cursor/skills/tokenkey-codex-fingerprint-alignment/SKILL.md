---
name: tokenkey-codex-fingerprint-alignment
description: Align TokenKey OpenAI Codex fingerprint versions and TLS profile with the installed Codex CLI. Use for local version drift, owner/alias consistency checks, version bumps, and local TLS/HTTP-2 capture against tk_canonical_codex_cli; non-version pins require separate evidence.
---

# TokenKey：Codex 指纹对齐

两条证据线，都由 `ops/openai/capture-codex-fingerprint.sh` 分发：

- HTTP 身份：读取本机 `codex --version` 与 native binary strings，对齐版本 owner（`capture_codex_fingerprint.py`）。
- TLS / HTTP-2 wire：真实 `codex exec` 连本地 CONNECT+TLS 采集端点（临时 `CODEX_HOME`、dummy key、每次新建本地 CA；CONNECT 只接受 `chatgpt.com` 且本地应答，不出网、不用真实 token），对比 `deploy/aws/stage0/tk_canonical_codex_cli.json`（`codex_tls_fingerprint.py`，ClientHello 解析、JA3 与 runtime digest 复用 `ops/kiro`）。只需 `openssl`，无需 mitmproxy 或登录。

## 修改边界

- 唯一可编辑版本 owner：`backend/internal/service/setting_gateway_runtime.go` 的 `DefaultOpenAICodexVersion`。UA、gateway `version`、usage probe `Version` 都从它派生，不新建 baseline JSON。
- Stage0 直打上游脚本 `ops/stage0/probe_openai_upstream_{model,image}.sh`、`probe_openai_image_fidelity_matrix.sh`、`probe_openai_image_ar_07_09.sh` 的 `CODEX_VERSION` 与 UA 内 `codex-tui/<ver>` 必须镜像同一版本（`check-consistency` 守卫）；`emit-edits` 会一并打出这些 mirror 文件的 bump。
- 版本 bump 保持 UA 的 OS/终端段原样；i18n `openaiCodexUserAgentPlaceholder`、`request.go`/测试里的格式示例不随版本修改。
- `originator=codex-tui`、`OpenAI-Beta: responses=experimental` 只读确认。binary strings 未找到不是漂移证据（可能运行时拼接）；上游 4xx 或实测明确换值才另行诊断，不自动修改。
- `/v1/messages` compat 桥接删除 originator/beta 是设计行为，不算 OAuth 出口指纹漂移。HTTP 身份不抓网络流量；TLS 只抓本地端点，不挂每日 hook。
- TLS owner：`tk_canonical_codex_cli.json` → 最新的 `tk_canonical_codex_cli` seed migration（现为 `tk_109`），由 `test_codex_tls_fingerprint.py` 逐字段守卫。bundle、CA 私钥、pcap 永不提交。

## 执行

```bash
codex --version
bash ops/openai/capture-codex-fingerprint.sh check env
bash ops/openai/capture-codex-fingerprint.sh diff
bash ops/openai/capture-codex-fingerprint.sh emit-edits
```

版本变化或 release watch 报 codex 漂移时，TLS 也要跑：

```bash
bash ops/openai/capture-codex-fingerprint.sh tls-capture --samples 3   # → .codex_tls/<stamp>-codex-cli-tls.bundle.json（ignored）
bash ops/openai/capture-codex-fingerprint.sh tls-check --bundle <bundle>
```

`tls-check` 判定规则（机械，不靠肉眼）：

| verdict | 条件 | 动作 |
|---|---|---|
| `aligned` (0) | 版本、runtime digest、HTTP/2/WSS wire 都一致 | 无 |
| `version_only` (1) | 只有 codex 版本变，digest 与 wire 不变 | `tls-emit-profile --bundle <bundle>` + changelog 一行；不加 migration |
| `tls_drift` (1)，digest 变 | cipher / extension 集合 / groups / key_share / sig algs / ALPN / PSK / versions / GREASE 变 | `tls-emit-profile` + **新增 forward migration** 更新同名行 + changelog 一行；不改已发布 migration |
| `tls_drift` (1)，digest 不变 | HTTP/2 SETTINGS / WINDOW_UPDATE / 伪头顺序或 WSS hello/path 变 | 改 Go wire owner（`repository/http_upstream_tk_codex_h2.go`、`service/openai_ws_client_tk_tls.go`）+ `tls-emit-profile` + changelog；不加 migration |
| `invalid` (2) | 样本不足、字段跨样本不一致、SNI 不对、bundle 含疑似凭证 | 修环境重跑，不改任何 artifact |

extension 顺序由 rustls 每次打乱，比较用排序后集合、JA4 与排序 JA3。约定细节见 `docs/ops/codex-fingerprint-changelog.md`。

已对齐就结束；否则按 emit-edits 只更新版本 owner。指定目标版本用 `emit-edits --version <version>`；需要机器输出用 `--json`。退出码：0 对齐/一致，1 漂移/派生断裂，2 用法/环境错误。

改后验证一次：

```bash
bash ops/openai/capture-codex-fingerprint.sh check-consistency
python3 -m unittest discover -s ops/openai -p 'test_*.py' -t ops/openai
./scripts/preflight.sh
```

`check-consistency` 只守卫版本派生契约，不对移动的上游版本判 CI 失败。纯版本字面量 bump 且无符号/锚点变化时使用 `upstream-touch-trivial` + `sentinel-registry-reviewed`；否则按实际 diff 判断。PR/合并遵循根规则。
