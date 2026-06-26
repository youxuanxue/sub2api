---
name: tokenkey-kiro-fingerprint-alignment
description: >-
  Capture and diff real Kiro IDE TLS JA3 and aws-sdk-js User-Agent against TokenKey constants. Use for Kiro TLS onboarding, suspected IDE JA3 drift, or pre-Phase-2 TLS gate verification; capture/diff only.
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
- **TLS（主，承重）**：`tcpdump` 被动抓握手 → `tshark` 解 ClientHello（明文，无需 MITM）→ JA3。
- **HTTP 协议（次）**：用 `probe_runtime_gateway.py` 读本机 token 直打网关验证。**mitm 实测不可行**
  —— Kiro IDE 直连网关、忽略 `HTTP_PROXY`，无代理可截获，故已移除 mitm 路径；UA 由常量已知。

## 工具（`ops/kiro/`）

- `capture-kiro-fingerprint.sh` — 被动 pcap 编排（`capture` / `diff` / `check` /
  `check-tls` / `show-baseline` / `emit-profile`）。
- `capture_kiro_fingerprint.py` — 确定性引擎（TLS/JA3-only）：重建期望 UA、解 tshark TSV、算 ja3
  （剥 GREASE、md5）、组 upstream 形态 profile、diff、退出码门禁。
- `probe-runtime-gateway.sh` / `probe_runtime_gateway.py` — 读本机 Kiro token，直打
  `runtime.us-east-1.kiro.dev` / `management.us-east-1.kiro.dev` 验证 HTTP 协议（无需 mitm）。
- `test_capture_kiro_fingerprint.py` / `test_probe_runtime_gateway.py` — 单测。

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
#   → 只写 deploy/aws/stage0/tk_canonical_kiro_ide.json（抓包/diff 基线 + provenance）
#   ⚠️ 更新线上 JA3 还需把 DB 行同步：首次由 migration tk_014 seed；后续更新走 admin
#      TLS profile UI，或新 migration 用 ON CONFLICT (name) DO UPDATE 重灌（tk_014 是
#      DO NOTHING 只初始化）。emit-profile 单独不改 DB。详见 design doc。

# 后续漂移检测（Kiro IDE 升级后）：
bash ops/kiro/capture-kiro-fingerprint.sh check --bundle .kiro_tls/<stamp>...bundle.json

# runtime.kiro.dev HTTP 协议探针（不用 mitm；需 Kiro 已登录）：
HTTPS_PROXY=http://127.0.0.1:7890 bash ops/kiro/probe-runtime-gateway.sh --refresh-token
# 只看请求形状：bash ops/kiro/probe-runtime-gateway.sh --dry-run all
# 指定模型：bash ops/kiro/probe-runtime-gateway.sh --refresh-token --model-id qwen3-coder-next runtime-chat
# TokenKey 形 UA 对比：bash ops/kiro/probe-runtime-gateway.sh --refresh-token --header-style tokenkey runtime-chat
```

## 边界

- 本 skill **只抓包 + diff + 落 profile**，**不开闸**。开闸（放开
  `IsTLSFingerprintEnabled` 给 kiro、migration seed、绑定账号、consistency test）
  是 Phase 2 PR，见 design doc。
- `tk_canonical_kiro_ide.json` **只能来自真实抓包**，不得捏造 JA3。
