---
name: tokenkey-antigravity-fingerprint-alignment
description: >-
  Capture a real Antigravity IDE (Google cloudcode-pa, the OAuth-relay platform)
  HTTP fingerprint via mitmproxy and diff it against TokenKey's Go constants
  (internal/pkg/antigravity/oauth.go, client.go, request_transformer.go). Inverted
  vs kiro: for antigravity the load-bearing signal is HTTP (impersonated client UA
  *version*, body `userAgent` literal, loadCodeAssist/onboardUser ideType metadata,
  privacy-endpoint `X-Goog-Api-Client: gl-node/<ver>`), NOT the TLS JA3 — TokenKey
  and the real IDE share a native Go/Node TLS stack so the ClientHello is
  same-origin and JA3 carries no signal (captured optionally, never gates). The
  cloudcode-pa endpoint is hard-coded (cannot be redirected like cc), so the IDE
  must egress through the mitm proxy and trust its CA. Use when an Antigravity IDE
  update is suspected of shifting the client UA version, or to refresh the
  impersonation constants. Capture + diff only; never fabricates a JA3.
---

# TokenKey：Antigravity 指纹对齐（mitm 抓 HTTP → diff Go 常量 → 改常量 → PR）

适用于本仓库（TokenKey fork of sub2api）。把 **真实 Antigravity IDE 流量** 当 ground
truth，**Go 常量** 当待对齐对象。**承重维度是 HTTP，不是 JA3**。

关联：`tokenkey-cc-fingerprint-alignment`（anthropic）、`tokenkey-kiro-fingerprint-alignment`
（kiro）、`tokenkey-fingerprint-alignment-all`（umbrella：三引擎合一 PR）、
`docs/antigravity-fingerprint-changelog.md`。

## 为什么与 cc / kiro 都不同

- cc 靠 `ANTHROPIC_BASE_URL` 重定向到自建 collector；kiro 端点硬编码、靠被动 pcap 抓 JA3。
- antigravity 端点 `cloudcode-pa.googleapis.com` 也硬编码（不能重定向），但**承重的是 HTTP
  层**：客户端 UA 的**版本号**、body 里 `userAgent:"antigravity"`、loadCodeAssist/onboardUser
  的 `ideType` metadata、隐私端点的 `X-Goog-Api-Client: gl-node/<ver>`。
- **JA3 不承重**：TK 与官方 IDE 都是 Go/Node 原生 TLS 栈，ClientHello 同源。故 TLS 走可选被动
  pcap、仅记录、**永不门禁**；不得从 UA 推断 JA3，也不得捏造 JA3。
- 采集机制 = **mitmproxy**：真实 IDE 必须走代理 + 信任 mitm CA。

## 对齐靶（diff 的 canonical 真值，全部读 Go 常量，无 baseline JSON）

| 维度 | 真值源 | 当前值 |
|---|---|---|
| HTTP UA 版本（承重） | `oauth.go` `DefaultUserAgentVersion` | `1.23.2` |
| UA 格式 | `oauth.go` `BuildUserAgent` | `antigravity/%s windows/amd64` |
| body userAgent | `request_transformer.go` | `antigravity` |
| ideType/ideName/platform/pluginType | `client.go` | `ANTIGRAVITY`/`antigravity`/`PLATFORM_UNSPECIFIED`/`GEMINI` |
| privacy `X-Goog-Api-Client` | `client.go` | `gl-node/22.21.1` |
| OAuth ClientID / 5 scopes | `oauth.go` | client_id + cclog/experimentsandconfigs 等 |

> UA 的 `windows/amd64` 是 TK **故意钉死**的（无论宿主 OS）。在 Mac 上抓到
> `darwin/arm64` 属预期、**不是漂移**——引擎把 os/arch 行标 `info`，只把**版本号**作承重比对。

## 工具（`ops/antigravity/`）

- `capture-antigravity-fingerprint.sh` — 编排（`check env` / `show-baseline` /
  `capture [--http] [--tls]` / `diff` / `check` / `check-tls`）。
- `capture_antigravity_fingerprint.py` — 确定性引擎：读 Go 常量、合并 mitm log、diff、退出码门禁。
- `mitm_antigravity_http_headers.py` — mitmproxy addon，抓 `v1internal:*` 请求 header + body identity。
- `test_capture_antigravity_fingerprint.py` — 单测。

## 流程

```bash
# 0) 前置:本机装好并登录 Antigravity IDE;装 mitmproxy(pipx install mitmproxy)
bash ops/antigravity/capture-antigravity-fingerprint.sh check env

# 1) 让 IDE 走 mitm 代理 + 信任 CA(二选一):
#    - IDE 设置 http.proxy=http://127.0.0.1:8080 且 http.proxyStrictSSL=false
#    - 或 HTTPS_PROXY=http://127.0.0.1:8080 NODE_EXTRA_CA_CERTS=~/.mitmproxy/mitmproxy-ca-cert.pem 启动
#    CA 文件:~/.mitmproxy/mitmproxy-ca-cert.pem(首次跑 mitmdump 自动生成),需进 OS/Node 信任库

# 2) 抓 HTTP(承重)。提示后在 IDE 发一次对话:
bash ops/antigravity/capture-antigravity-fingerprint.sh capture --http
#    可选叠加 TLS(非门禁):capture --http --tls

# 3) 门禁:
python3 ops/antigravity/capture_antigravity_fingerprint.py check --bundle .antigravity_fp/<stamp>-antigravity-capture.bundle.json
#    对齐 exit 0;UA 版本/body userAgent/ideType/gl-node 任一漂移 exit 1
```

## 漂移修复

- **仅 UA 版本漂移(最常见)**:改 `oauth.go` `DefaultUserAgentVersion` + 改
  `oauth_test.go` 的 `GetUserAgent()=="antigravity/<new> windows/amd64"` 断言 +
  `docs/antigravity-fingerprint-changelog.md` 追加一行。需**热推不发版**:推 admin 设置
  `antigravity_user_agent_version`(已接 `setting_service.GetAntigravityUserAgentVersion`
  + `wire.go`,运行时三级解析 admin→env→编译默认)。
- **gl-node / ideType / metadata 漂移**:改 `client.go` 对应常量 + 对应单测。
- **serving 路径缺 header**:若真机抓到官方在 `streamGenerateContent` 上发
  `X-Goog-Api-Client` / `Client-Metadata`(引擎标 `info`),补 header 是**另一个 PR**,不在本工具范围。

## 验证 / PR

```bash
python3 -m unittest discover -s ops/antigravity -p 'test_*.py' -t ops/antigravity
./scripts/preflight.sh
```

漂移修复后:分支 → commit(改 Go 常量属 `internal/pkg/antigravity/`,是 TK-owned 非 upstream)
→ `gh pr create`(TK 改动走 Squash and merge)。

## 边界 / 禁止

- 不开任何 DB TLS profile 闸(antigravity 无 JA3 门禁需求)。
- 不挂每日 sessionStart hook(漂移罕见、账号停服)。
- 不新增 canonical baseline JSON(单一真值源 = Go 常量)。
- 不捏造 JA3、不从 UA 版本推断 TLS。
- **失败回退**:若 IDE 不吃代理/CA(类 kiro 的 SDK pinning),HTTP 自动采集失败 → 退到被动
  pcap 仅取 JA3(非承重)+ 手动确认 UA;TUN 透明代理是后续可能,不在本工具内。
