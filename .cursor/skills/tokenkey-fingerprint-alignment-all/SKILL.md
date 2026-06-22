---
name: tokenkey-fingerprint-alignment-all
description: >-
  Run the combined TokenKey fingerprint refresh for Claude Code, Kiro, and Antigravity. Use when multiple client fingerprints need one orchestrated capture/diff report and one PR; use platform-specific skills for single-client refreshes.
---

# TokenKey：全平台指纹对齐（umbrella）

一次对齐**所有**客户端指纹，合一个 PR。三条采集引擎**机制不同必须独立**——cc 主动重定向到
自建 collector + cc0 MITM；kiro 被动 pcap（端点硬编码不可重定向）；antigravity 用 mitmproxy
抓 HTTP（承重的是 UA 版本/header/body，JA3 不承重）。本 skill 只统一**编排 + PR**。

关联：`tokenkey-cc-fingerprint-alignment`（cc 单平台）、`tokenkey-kiro-fingerprint-alignment`
（kiro 单平台）、`tokenkey-antigravity-fingerprint-alignment`（antigravity 单平台）、
`docs/accounts/kiro-tls-fingerprint-alignment-design.md`、`docs/antigravity-fingerprint-changelog.md`。

## 流程

```bash
# 跑三条引擎（各自前置条件不变：cc 需 cc0 栈；kiro 需 sudo + 真实 Kiro IDE；
# antigravity 需 mitmproxy + 真实 Antigravity IDE 信任 mitm CA）：
bash ops/fingerprint/capture-all-fingerprints.sh \
  --cc-arg --http \
  --kiro-arg --proxy-port --kiro-arg 7890 \
  --antigravity-arg --proxy-port --antigravity-arg 8080
#   → 末尾打印 combined drift report；退出码 1=有平台漂移，0=全齐/跳过，2=出错
# 只跑部分引擎：--skip-cc / --skip-kiro / --skip-antigravity
```

## 漂移后 → 一个 PR

按报告里哪个平台漂移，分别刷新其产物，**合并到一个 PR**：
- cc 漂移：编辑 `*-mimicry-baselines.json` / `constants.go` / `tk_canonical_cc_oauth.json`
  （遵循 cc skill 的 TLS↔HTTP 分轨纪律，禁止从 UA 推断 ja3）。
- kiro 漂移：`python3 ops/kiro/capture_kiro_fingerprint.py emit-profile --bundle <b>`
  → 刷新 `deploy/aws/stage0/tk_canonical_kiro_ide.json`。
- antigravity 漂移：bump `internal/pkg/antigravity/oauth.go` 的 `DefaultUserAgentVersion`
  + `oauth_test.go` 断言 + `docs/antigravity-fingerprint-changelog.md` 一行（JA3 不参与）。

然后 `scripts/preflight.sh` 全绿 → 一个分支、一个 PR 覆盖各平台的产物变更。

## 边界

- 不合并采集机制（cc redirect vs kiro pcap vs antigravity mitm）；不捏造任一平台的 ja3。
- 三平台漂移节奏不同（cc 频繁、kiro / antigravity 罕见）；若某次只有一个平台漂移，用对应单平台
  skill 即可，本 umbrella 用于「想一次性扫全平台 / 合一个 PR」的场景。
