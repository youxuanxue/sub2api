# Antigravity 2.15.1 本机采集与社区复核

采集／联网复核日期：2026-09-21 UTC。对象是 `/Applications/Antigravity.app`，目标仍是 OAuth 文本与生图服务，优先生图。本次更新采集记录与本地工具，没有修改运行时 UA/TLS 默认值或线上账号。

## 结论

**本机 App 从 2.14.0 升为 2.15.1，LS 二进制已更换；当前捕获只证明控制面握手未变，尚不能确认新版 cloudcode 生图指纹。** 官方模型出口的目标仍应是 Go LS；升级 Chrome 模板到本机 Chrome 153，不能据此提高与 LS 的一致性。

Manager 最新源码仍是 Chrome123 模拟路线，不能代表官方 IDE 最新、最稳定的客户端身份。官方 LS bridge 在保留官方传输和请求构造方面更有依据，但开源参考实现的当前协议与生图支持仍需适配。现有资料没有给出能解除 `anti-478 / VALIDATION_REQUIRED` 的同账号、同出口、生图对照实验。

## 本机新证据

| 项目 | 旧记录 | 本轮直接读取／捕获 | 评价 |
| --- | --- | --- | --- |
| App | 2.14.0 | **2.15.1**，bundle `com.google.antigravity` | 本机当前安装版本；不等于已验证的服务稳定版本 |
| Electron | 41.10.3 | **41.10.3** | UI 引擎未变，不能用它推导 LS 模型请求的 TLS |
| LS SHA-256 | `978c3352…f31` | `46a296d040163fd311948fbcb3ff0c9fb4ebf153083b4c17a89039e64fe85354` | 必须重新采集，不能仅修改版本标签 |
| LS 构建 | 旧摘要所录 build info | CL **984509791**；构建于 `2026-09-20 05:08:25 +0800` | `--stamp` 实测，时间不是采集时间 |
| Go 工具链 | go1.28 定制构建 | `go1.28-20260721-RC01 cl/951519500 +3ebc191975 X:fieldtrack,boringcrypto,simd,mapsplitgroup` | 工具链标识不变不代表二进制／完整网络栈不变 |
| App 启动身份 | hub / app.getVersion() | asar 仍传 `--subclient_type hub`、`--override_ide_version app.getVersion()`、`--override_user_agent_name antigravity` | 支持候选 UA `antigravity/hub/2.15.1 darwin/arm64`；**不是 HTTP 抓包** |
| App 启动端点 | daily cloudcode | `https://daily-cloudcode-pa.googleapis.com` | 启动默认值不证明全部请求最终只走 daily |
| 控制面 ClientHello | JA3 `03117a8ed39ef02427ebbc39f121275c` | 同值；`antigravity-unleash.goog` 两条原始握手 | 同轮两条样本字段一致，不能外推长期稳定或生图路径 |
| 当前 cloudcode / HTTP / 生图 | 历史摘要和待采集项 | **未捕获／未验证** | 不能把控制面样本升级成推理 profile |

机器报告：[antigravity-official-ide-2.15.1-20260921.json](antigravity-official-ide-2.15.1-20260921.json)。其中包含包版本、LS 与启动源码哈希、完整 `--stamp`、按 SNI 分组的握手字段及每条原始记录 SHA-256。

控制面样本无 GREASE，扩展顺序为 `[0,11,65281,23,18,5,10,13,50,16,43,51]`；ALPN 为 `h2,http/1.1`；key shares 为 `[4588,29]`，包含 X25519MLKEM768。另有三个 Playwright 下载域名样本使用同样的握手，报告单独分组，不将下载连接计入 cloudcode。

### 实验边界与失败结果

使用官方 LS 二进制和 App 的 hub 身份参数，在新建临时 `--gemini_dir` 内启动；没有复制用户登录状态。macOS `sandbox-exec` 限制出站仅 loopback；HTTP/HTTPS 代理指向本地 CONNECT sink。sink 返回 CONNECT 200 后只保存首条 TLS record，不向 Google 转发，也不完成 TLS 握手。不能从这个实验判断协商后的 TLS 版本、HTTP 协议、连接复用或服务成功率。

主要记录来自将 API/cloudcode 端点改到本地 HTTP sink 的启动实验；真实 SNI 的控制面连接仍通过 CONNECT 捕获。另一次保留 daily HTTPS 端点、调用本地 RPC 的诊断中：`GetUserSettings` 返回 unimplemented，`GetCodeAssistGlobalUserSetting` 本地路径返回 404，`GetUserStatus` 返回“未登录”的状态内容。仅向临时状态目录放置固定虚构 token 的进一步诊断也未使 LS 登录，没有产生 HTTP 事件；它不构成 OAuth 成功或已登录客户端采集。到此停止登录路径尝试，没有读取真实凭据。

三种诊断均未得到 cloudcode HTTP 或生图 ClientHello。这里的“未登录”仅指独立实验进程，**不表示用户 GUI App 未登录**。本次也没有用用户账号发起 Google 模型请求。

原始记录保留在本工作区的 `.antigravity_ide_fp/20260921-2.15.1-startup/`，已加入 Git ignore；仓库只保存解析后的安全字段与哈希。临时诊断日志不是长期证据 owner。

### 修订旧证据等级

旧 [2.14.0 cloudcode 报告](antigravity-official-ls-cloudcode-clienthello-20260921.json) 是按前一会话摘要整理的 JSON。本轮找不到相应 raw records，无法独立验证“8 条真实握手及稳定性”。其 JA3 字符串与哈希计算相符，只证明内部算术一致，不能证明来源。

因此将旧报告状态改为 `historical-summary-unverified`，保留历史字段供后续核对。旧调查文档也加了修订说明。**不应再称 cloudcode canonical 已由当前官方客户端验证。** TokenKey 当前分支的 IDE pin 仍是 `2.14.0`、cloudcode canonical 仍为无 ALPN；这是代码现状，既不是 2.15.1 实测结果，也不是 edge-us4 线上状态。是否调整运行时应依据补齐的目标路径证据。

## 社区当前源码比较

本轮通过 GitHub API 重新获取默认分支 HEAD，再按固定 SHA 下载源码。提交时间、文件哈希与 issue 元数据见 [来源清单](antigravity-community-sources-20260921.json)。代码说明“如何实现”，issue 说明“有人报告”，两者均不替代实测。

| 方案／本次 HEAD | 当前实际实现 | 与官方 2.15.1 的一致性及优劣 |
| --- | --- | --- |
| [Manager `61b95bd65c0a`](https://github.com/lbjlaq/Antigravity-Manager/blob/61b95bd65c0a9c694c1733e0e62f412e7a0a891a/src-tauri/src/proxy/upstream/client.rs#L164)，应用 4.7.11 | rquest 5.1.0、rquest-util 2.2.1 的依赖声明；`Emulation::Chrome123`；连接池复用 | 有可控的浏览器传输模板和运维功能，但不是官方 Go LS。声明版本并非本轮安装构建的依赖锁定结果，本轮也未重跑 Manager 抓包 |
| [Manager 身份策略](https://github.com/lbjlaq/Antigravity-Manager/blob/61b95bd65c0a9c694c1733e0e62f412e7a0a891a/src-tauri/src/constants.rs#L218) | 默认 UA 版本选 max(local,remote,4.3.0)，Chrome132 / Electron39 配置；macOS 长 UA 声称 Intel；附加 machine/session 和 x-client 头 | 4.7.11 是 Manager 应用版本，4.3.0 是身份策略下限，2.15.1 是本机官方 App 版本，不能混为一谈。源码“known stable”注释不是官方兼容承诺；本机 arm64 与其默认 Intel 身份也有差别 |
| [CLIProxyAPI `a5ab69521f7b`](https://github.com/router-for-me/CLIProxyAPI/blob/a5ab69521f7b4e0f244836d0419da8fcd89408ea/internal/runtime/executor/antigravity_executor.go#L240) | Antigravity 专用 Go transport，禁 H2，清空 ALPN；Hub 短 UA，读取 Hub manifest，fallback 2.9.1 | 相比 Chrome 模拟，更值得作为轻量 Go 对照。其“原生不发 ALPN”注释针对模型路径，不能拿本轮控制面 ALPN 推翻或证实；禁 H2 也不等于完整复刻 Google 定制 Go TLS |
| [opencode-antigravity-auth `16e0056431d0`](https://github.com/NoeFabris/opencode-antigravity-auth/blob/16e0056431d0a1291ee66e5938c732720b13a851/src/plugin/fingerprint.ts#L159) | `buildFingerprintHeaders` 在所查路径只返回 UA，主请求使用宿主 fetch | HTTP 身份管理较轻；叫 fingerprint 不代表有专门 TLS 模板，实际 TLS 由宿主决定 |
| [AIClient2API `b12e25ea2840`](https://github.com/justlovemaki/AIClient2API/blob/b12e25ea2840c880dd60f2ab4c45bc5bbfd10fab/src/providers/gemini/antigravity-core.js) | 默认短 UA `antigravity/2.8.1 darwin/arm64`；可选 sidecar 使用 `HelloChrome_Auto` | 可用于传输实验。Auto 由编译依赖决定，不跟随本机 Chrome 自动成为 153；是否启用 sidecar 必须另查实际运行配置 |
| [Tools-LS `d312237af838`](https://github.com/lbjlaq/Antigravity-Tools-LS/blob/d312237af83820ca15a636e81c5e61660bdf13f4/ls-orchestrator/src/native.rs#L350) | 官方 LS 进程 + 社区实现 Extension Server/Connect；HEAD 仍是 3/27 | 保留官方进程负责 TLS、HTTP、信封，方向更符合本任务。但适配的旧 Extension Server 启动方式与 2.15.1 Hub standalone 参数不同，不能直接宣称兼容；README 仍写不支持直接生图端点，只能工具间接触发 |

**推荐顺序：官方已登录 IDE 生图基线 → 适配当前官方 LS 的 bridge → 以实际 cloudcode 抓包校准 TokenKey 模拟。** Manager 可作独立对照，不能作为官方指纹 owner。这里的“官方 LS bridge”指“由 bridge 调用官方 LS”；Tools-LS 本身是社区软件，不是 Google 官方维护的网关。

相较之下，bridge 的代价是每账号进程／会话隔离、启动恢复、取消、流式结果和图片工件提取。轻量模拟的代价是持续维护 UA、TLS、HTTP 和信封的一致性。生图优先时，是否稳定返回可解码图片，比模型列表 200、版本号大或 JA3 对齐更有判断价值。

## 当前讨论与经验的评价

- [官方 CLI #785](https://github.com/google-antigravity/antigravity-cli/issues/785#issuecomment-5693871878)：最新所见后续报告是 macOS arm64 / CLI 1.2.4，手机验证后新进程仍 `VALIDATION_REQUIRED`，发生在 quota summary。其他回复建议核对账号关联地区；提问者表示已核对为日本仍失败。它表明原生程序也会遇到同类验证问题，不能由此断言只换 UA/TLS 就能解除。
- [Manager #2362](https://github.com/lbjlaq/Antigravity-Manager/issues/2362)：验证后再次要求验证。后续短链声称“自动化验证成功”，无可审计的机制或同条件对照，本轮不采信为技术解决证据。
- [Tools-LS #28](https://github.com/lbjlaq/Antigravity-Tools-LS/issues/28)：原生桥接也有 403 报告，但具体是缺 `cloudaicompanion.companions.generateChat` IAM 权限，**不是** `VALIDATION_REQUIRED`。应按 reason 和请求阶段分类，不能将所有 403 合并成指纹故障。
- [CLIProxyAPI #5287](https://github.com/router-for-me/CLIProxyAPI/issues/5287)：HTTPS 代理协商 h2 后却写 HTTP/1.1 CONNECT，导致 EOF；这是代理外层协议不一致的可复现实例，不是 Google 账号风险控制的证明。它支持“分清代理 TLS 与目标 TLS、保持协议一致”的工程原则。把某个 H2 网络错误修复称为解除账号验证，会混淆问题。

此次检索范围是上述项目当前默认分支、TLS／ALPN／verification 关键词 issue，以及相关评论；不宣称穷尽全社区。未发现固定账号、出口和真实生图任务，仅切换指纹并持续恢复的可靠证据。对 #24 的状态，本轮没有线上观测，因此不能给出恢复结论。

## 后续本地采集入口

新增包信息与已有原始握手的快照命令：

```bash
bash ops/antigravity/capture-official-ide-fingerprint.sh snapshot \
  --capture-dir .antigravity_ide_fp/20260921-2.15.1-startup \
  --out /tmp/antigravity-current-snapshot.json
bash ops/antigravity/capture-official-ide-fingerprint.sh check-version
```

仅采集包信息时省略 `--capture-dir`。快照只执行 LS 的 `--stamp` 元数据命令，不启动 LS 服务，也不会自动认定输入握手属于新安装版本；每轮必须用新目录并记录启动版本／哈希及时间，不能拿旧 raw 给新版本贴标签。当前 `check-version` 应报 installed=2.15.1、pinned=2.14.0 的漂移，这是如实提示，不是新版本验证已通过。

现有 `serve-tls` / `report-tls --sni ...` 继续用于按目标域名保存和解析 ClientHello；帮助文字已纠正：TLS 采集必须保留真实 HTTPS endpoint/SNI，把 `HTTPS_PROXY` 指向 sink；HTTP endpoint override 属于另一种实验。sink 会断开握手，不能拿它做在线生图成功率测试。

下一项真正缺失的是：已登录官方 App 的真实生图连接，在不终止目标 TLS 的被动路径中采集 cloudcode ClientHello；HTTP 身份另做受控采集。记录明确的模型、App/LS 版本、目标域名和图片结果后，才能决定是否更新 TokenKey cloudcode profile。当前静态候选 UA 与控制面样本不足以完成这一步。

验证：本机快照实际生成；`file` 确认 LS 为 arm64，`codesign --verify --deep --strict` 验证安装包签名完整性通过；原始 TLS 记录按 SNI 解析，与历史控制面样本比较的九项字段无差异；Python 采集测试 56 项通过；shell 语法检查通过。未跑生图成功率实验、未验证 TokenKey uTLS 的当前 on-wire 握手、未部署。
