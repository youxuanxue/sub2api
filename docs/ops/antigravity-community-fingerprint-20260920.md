# Antigravity 社区与 IDE 出口指纹调查（2026-09-20）

> 2026-09-21 证据修订：以 [2.15.1 更新采集与评价](antigravity-ide-2.15.1-community-20260921.md) 为准。
> 下文 2.14.0 cloudcode「8 次原始捕获」本轮找不到原始文件，不能独立复算，降级为待复核历史摘要；
> 不作为当前官方生图指纹已验证的结论。下文“与 TokenKey 主线的实际差异”表内 IDE 默认/profile
> 描述的是 PR #2265 分支修改后的状态，表头标作 `origin/main@9f544129d8` 不准确；也不代表线上配置。

调查时间：2026-09-20 UTC。针对 edge-us4 / #24 `anti-478` 的 `403 PERMISSION_DENIED / VALIDATION_REQUIRED`，核对社区当前实现与官方本机 App。本文是带日期的研究记录，不替代运行时指纹 owner，不改变账号或调度策略。

本地分支 `chore/antigravity0919` 已从原 HEAD 快进到本次 fetch 的 `origin/main@9f544129d8e136447fae957bc7826680c56b3bff`（1.8.242），包含 `agy 1.2.7` UA 更新。本轮把默认 Antigravity OAuth 路线切到官方 IDE/LS identity，并增加按 cloudcode SNI 隔离的 TLS profile、HTTP/TLS 本地采集脚本；CLI 与 Manager 仍可按账号显式选择。没有修改生产部署或调用 #24 的上游模型。

## 判断

1. **针对真人客户端生图，ground truth 应是官方 IDE / 官方 language_server 的真实出口；Antigravity-Manager 只是社区对照组，不是目标客户端。** 目前没有证据证明把 TokenKey 换成 IDE UA 或某个 TLS profile 就能解除 #24 的验证状态。
2. 社区不存在统一的 Antigravity TLS 方案：CLIProxyAPI 用 Go HTTP/1.1 且不发 ALPN；Manager 用 Chrome123 模拟；AIClient2API 可选 Chrome uTLS；若干插件只修改 HTTP 身份字段。不能把这些都称为“还原 IDE 指纹”。
3. 官方 App 的 UI 是 Electron，但模型请求由独立 Go `language_server` 发起。**Electron/Chrome 版本不等于模型出口 TLS 指纹。** 官方二进制构建也可能与普通 Go、第三方 uTLS 存在差异。
4. TokenKey 现有 CLI canonical TLS 样本来自未登录 CLI 的非推理域名；新增的官方 LS cloudcode profile 来自 8 次 `cloudcode-pa.googleapis.com` ClientHello，仍缺真实生图路径与当前版本已登录 HTTP 证据。
5. 原生官方 CLI 也有同样的验证循环报告。`VALIDATION_REQUIRED` 是 Google 返回的账号验证要求；它没有披露触发因素，不能从错误文案单独判断 TLS、IP、请求信封或账号资格哪个是根因。

## 证据与范围

- **代码证据**：本次联网读取 GitHub 默认分支 HEAD，再用固定 commit 下载实际调用路径。证明项目如何实现，不证明该方案能恢复 #24。
- **本机静态证据**：读取已安装官方 App 的 plist、asar 启动代码与 Go build info，没有启动 App 推理或读取账号凭据。
- **社区报告**：issue/PR 是作者观察，未在 #24 上复现；不把 PR 中的测试声明当作本次已运行的测试。
- 未取得当前 IDE 到 Google 生图端点的被动抓包；不发布推测的 IDE JA3/JA4。

## 各家实现比较

| 项目／固定 HEAD | TLS 与 HTTP 实现 | UA／身份处理 | 对本任务的价值与限制 |
| --- | --- | --- | --- |
| [CLIProxyAPI][cpa-tls] `61fdfc341b96`，9/19 | Antigravity 专用 Go transport；关闭 H2，清空 ALPN；按凭据隔离连接池并复用连接 | `antigravity/hub/<version> darwin/arm64`；从 Hub updater manifest 刷新版本，fallback `2.9.1` | 最值得比较的轻量模拟实现。其源码声称原生客户端不发 ALPN，但本次未取得该声明对应的当前版原始抓包；禁用 H2 不等于完整复刻 TLS |
| [Antigravity-Manager][manager-client] `477e11e7eb20`，9/19 | `rquest`，推理客户端选择 `Emulation::Chrome123` | `Antigravity/<version> (...) Chrome/... Electron/...`；版本取本地、远端、`4.3.0` floor 的较大值；发 `x-client-name/version`、`x-machine-id`、`x-vscode-sessionid` | 显式做了 TLS 模拟，但模拟的是 Chrome。UA 的 Chrome132/Electron39 与 Chrome123 transport 不是同一版本故事；其 floor 是项目策略，不是已验证的 Google 官方门槛 |
| [AIClient2API][aic-core] `b12e25ea2840`，9/16 | 默认 axios 路径；可选 Go uTLS sidecar，`HelloChrome_Auto`，按协商结果选择 H2/H1 | 源码默认 `antigravity/2.8.1 darwin/arm64` | 可借鉴独立传输实验能力；sidecar 必须启用、provider 在名单内且 ready 才生效。“有 sidecar 文件”不代表实际请求走 uTLS |
| [opencode-antigravity-auth][oc-request] `16e0056431d0`，8/27 | 主请求调用宿主 `fetch`；所查路径没有专用 Antigravity TLS 模板 | content 路径实际设置持久／会话 fingerprint 的短 UA；版本 fallback `1.18.3`，支持启动时设置版本。常量文件虽然有 Electron 长 UA，不能据此断言推理使用它 | 主要是 HTTP 身份与账号管理。`fingerprint` 包括设备／会话字段，不等于 TLS ClientHello；实际 TLS 取决于宿主运行时 |
| [antigravity-claude-proxy][badri-headers] `daa39d6c6239`，9/6 | Node 路径；代理配置使用 undici `ProxyAgent`，所查路径未见专用 TLS 复刻 | 读取本机 product.json：`ideVersion` 用于 UA，`version` 用于 `X-Client-Version`；fallback 分别 `2.0.3`、`1.110.0`；还发 `gl-node/18.18.2 fire/0.8.6 grpc/1.10.x` | 可学习版本字段区分，但其旧安装布局探测不一定覆盖当前 App；这些额外头不能未经抓包直接搬到 TokenKey |
| [dsh-agy][dsh-fp] `bd564c26a9f7`，9/18 | 所查 adapter 主要注入 headers，再委托 fetch helper；未见官方 LS 调用 | bundled 版本池仍是 `1.18.3/1.17.0/1.16.0`；提供随机或稳定身份模式、文件覆盖 | 最近有提交不代表指纹值新鲜。设备随机化不是已证实的验证恢复办法 |
| [Antigravity-Tools-LS][ls-native] `d312237af838`，3/27 | 真正启动官方 `ls_core`，在本地实现 Extension Server / Connect 协议，再由 LS 连接 Google | 启动参数、protobuf metadata 与 token 同步驱动原生 LS | 架构最接近“由 IDE 原生引擎负责出口”。但代码较旧，需要适配当前 LS；README 明确不支持直接生图端点，只能由其他模型调用工具间接生图 |
| [antigravity-proxy][ide-proxy] `32785d2098c9`，9/1 | Windows 进程网络 hook，通过 SOCKS/HTTP CONNECT 重定向真实 IDE / language_server 连接 | 不自己重造模型请求 UA／信封 | 适合作为“保持官方程序，只控制出口”的参考。是网络代理工具，不是 TokenKey 兼容 API 网关；只有隧道中不终止目标 TLS 时才保留客户端握手 |

补充：CLIProxyAPI 的 Anthropic/ChatGPT 路径有其他 uTLS 实现，**不能据此说它的 Antigravity 路径也用 Chrome uTLS**。本表跟踪的是 Antigravity executor 的实际 transport 构造。

## 当前官方 App：本机新证据

本次读取 `/Applications/Antigravity.app/Contents`：

- App：`2.14.0`，bundle ID `com.google.antigravity`。
- Electron framework：`41.10.3`。
- `Resources/app.asar` 内 `dist/languageServer.js` 启动 `Resources/bin/language_server`，参数包括：

```text
--standalone
--override_ide_name antigravity
--subclient_type hub
--override_ide_version <app.getVersion()>
--override_user_agent_name antigravity
--api_server_url https://generativelanguage.googleapis.com
--cloud_code_endpoint https://daily-cloudcode-pa.googleapis.com
--enable_sidecars
```

- `go version -m` 读到 LS 构建标识：`go1.28-20260721-RC01 cl/951519500 +3ebc191975 X:fieldtrack,boringcrypto,simd,mapsplitgroup`。
- LS SHA-256：`978c3352f64c2c6bd627640392539aa29c1123b7f95b1fdde024ded96f30cf31`。

这些静态证据支持关注 `hub` / Go LS 出口，而不是根据 Electron UI 选择 Chrome 模板。它们**不证明**当前模型请求的完整 UA、ALPN、ClientHello、目标域名或生图信封。端点参数也不保证所有请求最终只走该域名。

## 官方 LS 原始 ClientHello：本地隔离采集

随后用官方 `language_server` 连接本地 CONNECT 代理采集了控制面 2 次和 cloudcode SNI 8 次原始 TLS ClientHello。代理在返回 `200 Connection Established` 后只读取并保存客户端字节，**没有向 Google 转发**；独立启动的 LS 报告未登录，因此这不是生图请求抓包。

解析结果见 [antigravity-official-ls-clienthello-20260920.json](antigravity-official-ls-clienthello-20260920.json)。两次样本完全一致：

- TLS record version `0x0301`，ClientHello legacy version `771`；
- cipher suites、扩展顺序和扩展集合均稳定，JA3 为 `03117a8ed39ef02427ebbc39f121275c`；
- ALPN 为 `h2,http/1.1`，supported versions 为 `772,771`；
- supported groups 与 key share 都包含 `4588`（X25519MLKEM768）以及传统 `29`（X25519）；
- 没有观察到 GREASE 值；SNI 是 `antigravity-unleash.goog`。

控制面握手与 `tk_canonical_antigravity_cli` 相同，但 cloudcode transport 是独立 profile：JA3 `9b7dcdf3f997f1fb7b4409c94cb7ef36`，扩展列表不含 ALPN 16。该 profile 已用于默认 IDE 路线，但报告仍标记 `profile_replay_ready=false`：需要在已登录官方 App 中触发真实生图，并用同样的被动方法确认连接复用、HTTP 身份和请求时序。Manager Chrome123 与 Chrome153 浏览器指纹仍是独立实验组。

后续采集使用 `ops/antigravity/capture_official_ls_clienthello.py`：

```bash
python3 ops/antigravity/capture_official_ls_clienthello.py serve \
  --port 18080 --out-dir /tmp/antigravity-ls-capture
# 在另一个终端仅启动官方 LS，并将 HTTPS_PROXY 指向 http://127.0.0.1:18080
python3 ops/antigravity/capture_official_ls_clienthello.py report \
  --capture-dir /tmp/antigravity-ls-capture \
  --sni cloudcode-pa.googleapis.com \
  --out /tmp/antigravity-ls-capture/report.json
```

版本检查、TLS sink、SNI 报告和 HTTP 脱敏 sink 也可统一从
`ops/antigravity/capture-official-ide-fingerprint.sh` 调用；HTTP sink 只保留 UA、身份头和 IDE metadata，不保存 Authorization 值或完整请求体。

采集器按连接保存首个 TLS record 和目标主机索引，避免旧代理把多个连接无边界串接，也不会保存 CONNECT 请求头。`report` 只输出可比较的 ClientHello 元数据；更新 canonical profile 前仍需满足“已登录官方 App + 真实生图任务 + 目标 cloudcode SNI”的证据条件。

历史仓库记录曾于 6/13 捕获 IDE 2.0.11 的 `antigravity/hub/2.0.11 darwin/arm64`，但那只是历史 HTTP 证据；不能当成 2.14.0 生图 TLS 的实测。

## 生图要核对的不止 UA

[CLIProxyAPI 信封代码][cpa-body]将 image 模型设为 `requestType=image_gen`，使用 `image_gen/<timestamp>/<uuid>/12` requestId；自动补 `request.sessionId` 的分支主要处理非 image 请求。[AIClient2API][aic-core]也有相同风格的 image requestId。Manager 则在其 wrapper 中生成会话字段，再按类型决定 `image_gen` / `agent`。

因此，“某家生图成功”可能来自模型 ID、项目归属、OAuth client、session、requestType、工具调用路径或额度池差异，不能直接归因 TLS。请求头里的 UA、body `userAgent`、`ideType`、`sessionId`、`requestId` 是不同字段，不宜只换一个版本号拼接成混合身份。

Tools-LS README 的“原生连接完全相同”是项目自述：它调用官方二进制的事实可从代码确认，但其 metadata 注入、模拟 Extension Server、启动参数、账户与运行环境仍会影响行为。当前源码还专门收集 LS 的 `PERMISSION_DENIED` / `Verify your account` 错误；它不是绕过验证的保证。

## 与 TokenKey 主线的实际差异

| 维度 | `origin/main@9f544129d8` 的状态 | 本次判断 |
| --- | --- | --- |
| HTTP UA | 默认 `antigravity/hub/2.14.0 darwin/arm64`；CLI 显式兼容路线仍为 `antigravity/cli/1.2.7 darwin/arm64` | Hub 格式由官方 App 启动参数和历史已登录 IDE on-wire 证据支持；当前 2.14.0 已登录生图 HTTP 仍待本地采集 |
| TLS profile | 默认 `tk_canonical_antigravity_ide_cloudcode`，无 GREASE／无扩展随机化／无 ALPN；CLI profile 保留 `h2,http/1.1` | 官方 LS 控制面与 cloudcode transport 已分离；新 profile 只代表 cloudcode SNI transport，不能把未登录样本称为生图成功证据 |
| TLS 样本来源 | CLI：`agy 1.2.2`，5 个样本，未登录；IDE cloudcode：8 个样本，未登录，SNI 为 `cloudcode-pa.googleapis.com` | 证明两类握手存在且 cloudcode 样本稳定。**不能证明未登录进程与已登录生图连接在连接复用、请求时序上完全相同** |
| profile 生效条件 | `ResolveTLSProfile` 先检查账号是否启用 TLS fingerprint，再处理显式绑定／随机／按名 canonical fallback | 仓库有 JSON 不等于 #24 正在使用它。未读取 edge-us4 当前部署与账号配置，所以不能把源码状态称为线上状态 |
| 生图分类 | `request_type.go` 已将 image 分为 `image_gen` | 这个字段已存在仍不等于完整 IDE 生图信封已复刻；额度池注释也不是上游权益证明 |
| 证据约定 | capture 脚本与 changelog 开头仍写 JA3 “non-load-bearing”；近期又加入 TLS seed | 仓库说明存在证据口径不一致。“TLS 无影响”与“换 TLS 能解决”两种结论目前都过强 |

对应本地文件：

- `backend/internal/pkg/antigravity/oauth.go`
- `backend/internal/pkg/antigravity/request_type.go`
- `backend/internal/service/tls_fingerprint_profile_service.go`
- `deploy/aws/stage0/tk_canonical_antigravity_cli.json`
- `backend/internal/pkg/tlsfingerprint/antigravity_alpn_http2_test.go`
- `ops/antigravity/capture_antigravity_fingerprint.py`
- `docs/ops/antigravity-fingerprint-changelog.md`

## 对 VALIDATION_REQUIRED 的外部证据

1. [官方 CLI issue #785](https://github.com/google-antigravity/antigravity-cli/issues/785)：报告原生 CLI OAuth 登录成功、拿到 token，但 `retrieveUserQuotaSummary` 返回“Verify your account”；正常和无痕浏览器验证均未恢复。
2. [该 issue 的后续报告](https://github.com/google-antigravity/antigravity-cli/issues/785#issuecomment-5693871878)：CLI 1.2.4、个人账号；手机验证／浏览器 success 后，新 CLI 进程仍 `VALIDATION_REQUIRED`。这是同类现象，不是 #24 的根因证明。
3. [Manager issue #2362](https://github.com/lbjlaq/Antigravity-Manager/issues/2362)：验证后再次调用又要求验证。同帖外部短链“自动化验证成功”的回复缺乏可审计机制与复现证据，本次没有采纳或访问其服务。
4. [codex-antigravity-auth PR #31](https://github.com/Reedtrullz/codex-antigravity-auth/pull/31) 将可恢复验证错误与永久封禁分开处理；[opencodex PR #5099](https://github.com/lidge-jun/opencodex/pull/5099) 提供隔离／切换／后续探测。它们是错误处理与可用性策略，不是 TLS 恢复证据。

本次所查资料中，**没有找到“同一账号、同一出口、同一生图任务，仅改变 TLS/UA，持续解除 VALIDATION_REQUIRED”的可靠对照证据**。这不证明指纹无关，只限定当前结论强度。

## 建议的验证顺序（本次未执行）

1. **先确认官方 IDE 真实生图基线。** 同一个 #24 Google 账号、同一 edge-us4 出口，用当前官方 App 触发真实图片生成。保持纯 SOCKS/CONNECT/TUN 隧道，不让中间层终止目标 TLS。记录 App/LS 版本、目标域名、模型、时间和实际图片结果。若模型由 IDE 工具选择，记录实际模型，不宣称与直调输入完全相同。
2. **先被动看 TLS，再独立看 HTTP。** 被动采集真实生图连接的 SNI、TLS extensions/顺序、ALPN、key shares、协议和连接复用；另一次受控 HTTP 解密只用于看头与信封。MITM 会改变 Google 看到的 TLS，不能把 MITM 上游握手当作官方 LS 握手。
3. **读取线上真实配置。** 核对 us4 部署 SHA、#24 的 TLS 开关与 profile、UA override、OAuth client、上游 host 与代理出口。只看 canonical 文件不能完成这一判断。
4. **原生 IDE 若也被拒绝：** 优先排查官方验证结果／账号资格与出口影响。完成官方验证后重测实际生图；浏览器 auth-success、refresh 成功、模型列表 200 都不能作为恢复验收。
5. **原生 IDE 若成功而 TokenKey 失败：** 固定账号、出口、目标模型／任务、时间窗口，先比较完整客户端身份与请求路径。再分别控制传输、HTTP 头、信封等变量。协议组合必须与真实客户端保持一致，不盲目混搭 CLI UA、Chrome TLS 和 Hub metadata。
6. **需要可服务的原生引擎候选时：** 优先评估官方 LS bridge（Tools-LS 是参考实现）能否适配 2.14.0、支持生图工具结果、会话隔离与取消。CLIProxyAPI 适合作为较轻的模拟对照；Manager / Chrome sidecar 可列入实验组，暂不作为“更像 IDE”的默认结论。

验收标准：真实返回可解码图片／有效图片工件，且归属预期账号与模型；`403` 消失或单次 HTTP 200 不足够。

## 固定源码索引

以下链接固定到本次读取的 commit。临时下载与本机静态摘录在 `/tmp/tokenkey-antigravity-research-20260920`，不依赖它们长期保存即可通过链接复核代码。

[cpa-tls]: https://github.com/router-for-me/CLIProxyAPI/blob/61fdfc341b96178a8dcb53f2efc46cbc341d267c/internal/runtime/executor/antigravity_executor.go#L240
[cpa-body]: https://github.com/router-for-me/CLIProxyAPI/blob/61fdfc341b96178a8dcb53f2efc46cbc341d267c/internal/runtime/executor/antigravity_executor_request.go#L459
[manager-client]: https://github.com/lbjlaq/Antigravity-Manager/blob/477e11e7eb20a9b8d3234babf2457ccd100c04ed/src-tauri/src/proxy/upstream/client.rs#L164
[aic-core]: https://github.com/justlovemaki/AIClient2API/blob/b12e25ea2840c880dd60f2ab4c45bc5bbfd10fab/src/providers/gemini/antigravity-core.js
[oc-request]: https://github.com/NoeFabris/opencode-antigravity-auth/blob/16e0056431d0a1291ee66e5938c732720b13a851/src/plugin/request.ts#L1540
[badri-headers]: https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/src/constants.js#L102
[dsh-fp]: https://github.com/chaos-03x/dsh-agy/blob/bd564c26a9f7d6fa597c0f1081841551ad715707/src/runtime/fingerprint.ts
[ls-native]: https://github.com/lbjlaq/Antigravity-Tools-LS/blob/d312237af83820ca15a636e81c5e61660bdf13f4/ls-orchestrator/src/native.rs#L351
[ide-proxy]: https://github.com/yuaotian/antigravity-proxy/blob/32785d2098c9a07c6b7060f47d04a17ac0e5a431/README_EN.md

进一步复核：

- [CLIProxyAPI Hub UA 与版本解析](https://github.com/router-for-me/CLIProxyAPI/blob/61fdfc341b96178a8dcb53f2efc46cbc341d267c/internal/misc/antigravity_version.go)
- [Manager UA／版本 floor](https://github.com/lbjlaq/Antigravity-Manager/blob/477e11e7eb20a9b8d3234babf2457ccd100c04ed/src-tauri/src/constants.rs)
- [AIClient2API TLS sidecar](https://github.com/justlovemaki/AIClient2API/blob/b12e25ea2840c880dd60f2ab4c45bc5bbfd10fab/tls-sidecar/main.go) 与 [启用条件](https://github.com/justlovemaki/AIClient2API/blob/b12e25ea2840c880dd60f2ab4c45bc5bbfd10fab/src/utils/proxy-utils.js#L216)
- [opencode 实际 fingerprint headers](https://github.com/NoeFabris/opencode-antigravity-auth/blob/16e0056431d0a1291ee66e5938c732720b13a851/src/plugin/fingerprint.ts#L159)
- [claude-proxy 版本探测](https://github.com/badrisnarayanan/antigravity-claude-proxy/blob/daa39d6c6239ac078a4e69de85094dde35558ef6/src/utils/version-detector.js)
- [dsh-agy bundled 版本池](https://github.com/chaos-03x/dsh-agy/blob/bd564c26a9f7d6fa597c0f1081841551ad715707/src/runtime/fingerprint-data.json)
- [Tools-LS 生图限制](https://github.com/lbjlaq/Antigravity-Tools-LS/blob/d312237af83820ca15a636e81c5e61660bdf13f4/README.md#L213)

检查：固定 commit 与下载 metadata 对照；所引源码文件存在；本机版本与启动参数直接读取。仅新增调查文档，未运行模型探测或运行时代码测试。
