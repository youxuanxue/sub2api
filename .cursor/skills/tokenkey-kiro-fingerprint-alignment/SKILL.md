---
name: tokenkey-kiro-fingerprint-alignment
description: >-
  Capture a real Kiro IDE (AWS CodeWhisperer, sixth platform) TLS ClientHello by
  passive pcap and diff its JA3 + aws-sdk-js User-Agent against TokenKey repo
  constants (kiro/constants.go, tk_canonical_kiro_ide). Unlike cc, the Kiro IDE
  endpoint is hard-coded and cannot be redirected to a collector, so capture is
  tcpdump + tshark (handshake is plaintext); HTTP UA via mitm is best-effort.
  Use when onboarding the canonical Kiro TLS profile, after a Kiro IDE update is
  suspected of shifting the JA3, or before opening the Phase-2 PR that enables
  IsTLSFingerprintEnabled for kiro. This skill is capture + diff only; it never
  fabricates a JA3 and never opens the TLS gate.
---

# TokenKey：Kiro 指纹对齐（被动抓包 → diff → 落 profile）

适用于本仓库（TokenKey fork of sub2api）。把 **真实 Kiro IDE 流量** 当 ground truth，
**kiro 常量 + DB TLS profile** 当待对齐对象。**禁止从 UA 版本号推断 ja3**。

关联：`docs/accounts/kiro-tls-fingerprint-alignment-design.md`（两阶段设计 + Phase 2 开闸清单）、
`tokenkey-cc-fingerprint-alignment`（anthropic 平行 skill）、
`tokenkey-fingerprint-alignment-all`（umbrella：cc+kiro 一次对齐、合一个 PR）。

## 为什么与 cc 不同

cc 靠 `ANTHROPIC_BASE_URL` 重定向到自建 collector + MITM。Kiro IDE 端点
`codewhisperer.us-east-1.amazonaws.com` 硬编码、无法重定向。故：
- **TLS（主）**：`tcpdump` 被动抓握手 → `tshark` 解 ClientHello（明文，无需 MITM）→ JA3。
- **HTTP UA（次，best-effort）**：仅当 Kiro IDE 尊重 `HTTP_PROXY`+受信 CA 时 mitm 验证；
  否则 JA3 是承重信号，UA 已由常量已知、手动确认即可。

## 工具（`ops/kiro/`）

- `capture-kiro-fingerprint.sh` — 被动 pcap 编排（`capture` / `diff` / `check` /
  `check-tls` / `show-baseline` / `emit-profile`）。
- `capture_kiro_fingerprint.py` — 确定性引擎：重建期望 UA、解 tshark TSV、算 ja3
  （剥 GREASE、md5）、组 upstream 形态 profile、diff、退出码门禁。
- `mitm_kiro_http_headers.py` — 可选 UA 验证 addon。
- `test_capture_kiro_fingerprint.py` — 单测。

## 流程

```bash
# 在跑着真实、已登录 Kiro IDE 的机器上（tcpdump 需 sudo）：
# 直连出口：
bash ops/kiro/capture-kiro-fingerprint.sh capture --iface en0 --seconds 60
# 走系统代理（Electron 跟随系统代理，如 Clash:7890）——抓 loopback 上明文 ClientHello：
bash ops/kiro/capture-kiro-fingerprint.sh capture --proxy-port 7890 --seconds 75
#   → 提示时在 Kiro IDE 触发一次请求；首抓为 missing_tokenkey（非阻断）

# 用真实抓包落盘 canonical profile：
python3 ops/kiro/capture_kiro_fingerprint.py emit-profile --bundle .kiro_tls/<stamp>-kiro-capture.bundle.json
#   → 写 deploy/aws/stage0/tk_canonical_kiro_ide.json

# 后续漂移检测（Kiro IDE 升级后）：
bash ops/kiro/capture-kiro-fingerprint.sh check --bundle .kiro_tls/<stamp>...bundle.json
```

## 边界

- 本 skill **只抓包 + diff + 落 profile**，**不开闸**。开闸（放开
  `IsTLSFingerprintEnabled` 给 kiro、migration seed、绑定账号、consistency test）
  是 Phase 2 PR，见 design doc。
- `tk_canonical_kiro_ide.json` **只能来自真实抓包**，不得捏造 JA3。
