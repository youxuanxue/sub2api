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
| HTTP UA 版本（承重） | `oauth.go` `DefaultUserAgentVersion` | `2.0.11`（2026-06-13 on-wire 对齐；随 IDE 自更新，走热推）|
| UA 格式 | `oauth.go` `BuildUserAgent` | `antigravity/hub/%s windows/amd64`（`hub`=subclient_type，on-wire 确认必带）|
| body userAgent | `request_transformer.go` | `antigravity` |
| ideType/ideName/platform/pluginType | `client.go` | `ANTIGRAVITY`/`antigravity`/`PLATFORM_UNSPECIFIED`/`GEMINI` |
| privacy `X-Goog-Api-Client` | `client.go` | **不发**（2026-06-13 on-wire：真实 IDE 在 setUserSettings/fetchUserInfo 上不发 gl-node，已移除）|
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

## 用 Antigravity CLI（`agy`）采集（真机已验 2026-06-11）

本机没装 IDE 时，可改用 **Antigravity CLI**（`agy`，brew cask `antigravity-cli`；`agy0-here`
经 cc0 指纹链启动）触发真实 `v1internal:*` 请求。**与 IDE 路径有两处实测差异**：

1. **CA 信任：`agy` 是 Go 二进制（go1.27），Go 在 macOS 的 TLS 校验只认系统/login 钥匙串，
   忽略 `SSL_CERT_FILE` 和 `NODE_EXTRA_CA_CERTS`**（上面 IDE 流程第 1 步的 Node 变量对 CLI 无效）。
   把 mitm CA 信任进 **login keychain（免 sudo，弹一次 GUI 授权）**，采集后移除：
   ```bash
   security add-trusted-cert -r trustRoot -k "$HOME/Library/Keychains/login.keychain-db" \
     "$HOME/.mitmproxy/mitmproxy-ca-cert.pem"
   # …采集完务必撤销：
   security delete-certificate -c mitmproxy -t "$HOME/Library/Keychains/login.keychain-db"
   ```
2. **出口保持在指纹链**：mitmproxy 用 **upstream 模式串到 gost**（`agy0` 用的 cc0 链
   `:11800 → socks5 :1114 → CC0_EXPECT_EGRESS_IP`），而非自己直连：
   ```bash
   ANTIGRAVITY_CAPTURE_HTTP_LOG=/tmp/ag-http.jsonl \
     mitmdump -p 8080 --mode upstream:http://127.0.0.1:11800 \
     -s ops/antigravity/mitm_antigravity_http_headers.py &
   # 在【空目录】发一次 print（否则 agy 会去索引当前大仓库、迟迟不发网络请求像假死）：
   ( cd "$(mktemp -d)" && HTTP_PROXY=http://127.0.0.1:8080 HTTPS_PROXY=http://127.0.0.1:8080 \
       agy --print "Reply with one word: pong" --print-timeout 60s </dev/null )
   # 之后照常 bundle-from-artifacts + check（mitm log 路径同上）
   ```

**CLI ≠ IDE，是不同的客户端指纹**（引擎 baseline 对标 IDE，喂 CLI 抓包必然报「drift」，
属预期，**不是 TK 回归**）。实测对照：

| 维度 | CLI 实测（`agy` 1.0.7 / Mac） | IDE 基线（TK 镜像对象） |
|---|---|---|
| UA | `antigravity/cli/1.0.7 darwin/arm64`（多 `/cli/` 段、Go 非 Node） | `antigravity/1.23.2 windows/amd64` |
| body `userAgent` | `antigravity` ✓ 同 | `antigravity` |
| `ideType` | `ANTIGRAVITY` ✓ 同 | `ANTIGRAVITY` |
| `X-Goog-Api-Client` | **空**（Go 不发 gl-node 头） | `gl-node/22.21.1` |
| 端点 host | `daily-cloudcode-pa.googleapis.com` | `cloudcode-pa.googleapis.com` |
| model（streamGenerateContent）| `gemini-3.5-flash-low` / `gemini-3.1-flash-lite` | — |

> 引擎的 UA 版本正则是 `antigravity/<ver>`，匹配不了 CLI 的 `antigravity/cli/<ver>` → 标
> `ua_version: missing_capture`；`platform` 会报 `DARWIN_ARM64` vs `PLATFORM_UNSPECIFIED`。
> 要把 CLI 也纳入对齐需单独扩 baseline；当前 TK 仅镜像 **IDE**，CLI 抓包用于交叉验证工具链。

## 真实 IDE 校验（无需 on-wire；2026-06-12 实证）

想直接抓**真实 Antigravity IDE**（`brew install --cask antigravity`，2.0.11）的 on-wire 流量会**撞墙**，记录如下省得重踩：

- IDE 的 Go `language_server`（真正的 cloudcode-pa 客户端）**直连 Google、无视一切 HTTP 代理**——`HTTP(S)_PROXY` 环境变量、macOS 系统代理、`.zshrc`、VS Code `http.proxy` 设了都没用，它照样 `dial tcp <google-ip>:443`。所以 **proxy-env 的 mitm 抓不到 IDE**；唯一能抓的是系统级 TUN（如 ClashX Pro 增强模式把直连引到本地 mitmproxy），重且脆。
- **不用抓也能校验**——IDE 启动 `language_server` 的命令行就是权威身份来源（IDE 主进程日志 / `ps` 可见）：
  ```
  language_server --standalone --override_ide_name antigravity --subclient_type hub \
    --override_ide_version 2.0.11 --override_user_agent_name antigravity \
    --cloud_code_endpoint https://daily-cloudcode-pa.googleapis.com
  ```
  配合 `strings <language_server>` 看二进制里的字面量，即可逐项核对，无需联网。

**2026-06-13 on-wire 真机抓包结论（IDE 2.0.11，已 36 条样本对齐，推翻 06-12 的 spawn 参数推断）：**

> ⚠️ 06-12 仅靠 spawn 参数 + 二进制 strings **误判了两处**——只有 on-wire 抓包才看得出。抓法：IDE → mitmproxy(8080) → sing-box(trojan 桥) → SG 出口 → Google，靠 **ClashX TUN** 抓 `language_server` 的直连（它无视 HTTP 代理）。**坑：sing-box 桥必须在 TUN 开启前先连好 SG，否则它到 SG 服务器的连接被 TUN 套回 SG-trojan 形成 trojan-套-trojan 回环 → EOF；`IP-CIDR <sg-ip> DIRECT` 规则在 ClashX premium TUN 下不一定拆得开。**

| 维度 | TK 原常量 | 真实 IDE on-wire | 判定/动作 |
|---|---|---|---|
| UA | `antigravity/%s windows/amd64` | `antigravity/hub/2.0.11 darwin/arm64` | ⚠️ **缺 `/hub/` 段** → 改为 `antigravity/hub/%s …`（06-12 误判为「格式正确」）|
| UA 版本 | `1.23.2` | `2.0.11` | ⚠️ bump（见下）|
| `X-Goog-Api-Client` | `gl-node/22.21.1`（setUserSettings/fetchUserInfo）| **不发** | ⚠️ 06-12 误判「现行」；真机这两端点不发 → **移除** |
| body userAgent | `antigravity` | `antigravity` | ✅ |
| ideType | `ANTIGRAVITY` | `ANTIGRAVITY` | ✅ |
| platform | `PLATFORM_UNSPECIFIED` | `DARWIN_ARM64`(真机机型) | 暂留 PLATFORM_UNSPECIFIED（与 windows/amd64 钉死策略一致；要钉 Windows 枚举需 Win 机抓包）|
| os/arch | `windows/amd64`(钉死) | `darwin/arm64`(本机) | info，仍钉死 |

> **版本可以 bump，但记住它是移动目标。** `2.0.11` = IDE 的 app 版本，每次自更新就变。本次随 `/hub/`+gl-node 一起把编译默认 bump 到 2.0.11（与现役一致）；后续新版本优先走 admin 热推 `antigravity_user_agent_version`（运行时 overlay），别为每个小版本都改 oauth.go。

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
