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
- Codex：读取安装的 CLI binary/static identity，门禁是 `check`；TLS lane 是真实
  codex 连本地采集端点的 `tls-capture`（`--skip-codex-tls` 可跳过，覆盖记为 incomplete）。

- Gemini CLI、Grok CLI：调用各自现有 `capture` 引擎，记录实际安装版本与 bundle 路径；
  保持各自唯一 pin owner，不用 release metadata 替代本机证据。

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

目标与 companion/advisory 策略从 identity registry 派生；每个 required identity 必须
有本轮明确结果，缺引擎或漏采记 incomplete。原始引擎输出保留版本与证据出处，
每条 lane 的本轮 engine log 保存在 `.cache/fingerprint/all.*`；退出码 0 本身
不能代替有效证据。聚合前校验 bundle 的本轮文件与采集时间、实际版本及既有
pin reader；缺证据或旧 bundle 记 incomplete，版本与 pin 不匹配记 drift。

禁止把 rc=2/3 报成“全部已对齐”。Claude Code 仍须机械分类 auth cohort；Kiro CLI
仍须四条 evidence lanes 完整。

## 漂移后

- Claude Code：遵循 `tokenkey-cc-fingerprint-alignment` 的 TLS/HTTP 分轨规则。
- Kiro CLI：遵循 `tokenkey-kiro-fingerprint-alignment`；真实 bundle 刷新
  `deploy/stage0/tk_canonical_kiro_cli.json`，HTTP 只改唯一 CLI owner，随后完成
  TokenKey/uTLS replay semantic gate 与 DB parity 投影。不得恢复替代客户端模式。
- Antigravity：遵循其真实 HTTP evidence skill。
- Codex、Gemini CLI、Grok CLI：各走对应单平台 skill 与唯一 pin owner。

所有平台改动可进入一个 PR，但每条证据 provenance 与验证结果必须独立记录。最终运行
focused tests、sentinel、`scripts/preflight.sh` 和 `make test`。

## 边界

- 不合并不同采集机制，不从版本推断 wire 字段，不捏造 JA3。
- 只有一个平台漂移时优先用单平台 skill；本 skill 用于全局扫描或合并交付。
- 原始 capture、凭证、token cache、pcap 和 mitm logs 永不提交。
