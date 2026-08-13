---
name: tokenkey-fingerprint-alignment-all
description: >-
  Run the combined TokenKey fingerprint refresh for Claude Code, Kiro CLI, Antigravity, Codex, Gemini CLI, and Grok CLI. Enter after client-release-watch reports drift, or when capture-all finds actionable fingerprint drift; use platform-specific skills for single-client refreshes.
---

# TokenKey：全平台指纹对齐（umbrella）

## 两层入口

1. `bash ops/fingerprint/client-release-watch.sh scan --plan`：只读发现上游 release，
   对比 registry 声明的唯一 compile pin，不 capture、不写状态。
2. `bash ops/fingerprint/capture-all-fingerprints.sh`：逐平台运行真实证据引擎并聚合
   退出码；身份 owner、release source 与证据层级只登记在
   `scripts/fingerprint/client_identity_registry.json`。

各平台采集机制保持独立：

- Claude Code：真实 CLI + collector/MITM，TLS 与 HTTP 分轨。
- Kiro CLI：真实、已登录 `kiro-cli` + mitm collector，同时观察 TLS、HTTP、协议和
  auth cohort；rustls extension order 以 semantic projection 比较。
- Antigravity：真实客户端 HTTP MITM；TLS 非承重。
- Codex：读取安装的 CLI binary/static identity；门禁是 `check`。

umbrella 只负责编排和一个合并变更，不把 release metadata、static evidence、wire
capture、runtime identity 或 production-configured observation 混成同一证据层。

## 流程

```bash
bash ops/fingerprint/capture-all-fingerprints.sh \
  --cc-arg --http \
  --kiro-arg --samples --kiro-arg 3 \
  --antigravity-arg --proxy-port --antigravity-arg 8080
```

状态只能是 `aligned / drift / incomplete / skipped / error`：

- `0`：所有要求证据已观察且对齐。
- `1`：至少一条 semantic drift。
- `2`：invalid evidence 或执行失败。
- `3`：显式 skip 或 evidence `NOT_OBSERVED`。

禁止把 rc=2/3 报成“全部已对齐”。Claude Code 仍须机械分类 auth cohort；Kiro CLI
仍须四条 evidence lanes 完整。

## 漂移后

- Claude Code：遵循 `tokenkey-cc-fingerprint-alignment` 的 TLS/HTTP 分轨规则。
- Kiro CLI：遵循 `tokenkey-kiro-fingerprint-alignment`；真实 bundle 刷新
  `deploy/aws/stage0/tk_canonical_kiro_cli.json`，HTTP 只改唯一 CLI owner，随后完成
  TokenKey/uTLS replay semantic gate 与 DB parity 投影。不得恢复替代客户端模式。
- Antigravity：遵循其真实 HTTP evidence skill。
- Codex、Gemini CLI、Grok CLI：各走对应单平台 skill 与唯一 pin owner。

所有平台改动可进入一个 PR，但每条证据 provenance 与验证结果必须独立记录。最终运行
focused tests、sentinel、`scripts/preflight.sh` 和 `make test`。

## 边界

- 不合并不同采集机制，不从版本推断 wire 字段，不捏造 JA3。
- 只有一个平台漂移时优先用单平台 skill；本 skill 用于全局扫描或合并交付。
- 原始 capture、凭证、token cache、pcap 和 mitm logs 永不提交。
