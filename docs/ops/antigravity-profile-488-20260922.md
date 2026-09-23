# profile-488 官方 Antigravity 采集分析

采集包：`ops/antigravity/profile-488-capture.zip`。分析时间：2026-09-22 UTC。raw record 在临时目录独立解析，未把原始连接字节复制进仓库；归一化结果见 [antigravity-profile-488-20260922.json](antigravity-profile-488-20260922.json)。

## 采集限制归因（WIP）

报告不能证明同事没有点击生图、没有登录或 LS 从未向 Google 发包，只能证明这些事件没有进入本次证据。采集器的非转发 sink 会主动打断全部代理连接，可能阻止启动／资格检查完成；部分流量也可能未经过 sink。HTTP phase 缺失可能与运行参数或 Linux 适配版本有关，但 ZIP 未包含脚本版本、源码或 argv，无法确定。应先修正采集流程并获得实际 Linux 适配器，不能把缺证据归因于同事操作。

## 采集是否完整

这是一个有效的 `full-local-collector` 报告，脱敏标记为 token、cookie、Authorization 值均未保存；TLS raw record 在压缩包中存在。机器是 Linux ARM64/OrbStack，**不是 macOS**：

| 项目 | 结果 |
| --- | --- |
| App | 1.107.0，stable，commit `15487b3041e65228cae24980a3f796c905ef582c` |
| IDE/LS | 1.23.2；构建 CL 900566399；LS SHA256 `a4fbb73985e7f2eb28d8f41d241bc49c0f6ce504f88b415f611bb3e633c9ac71` |
| Electron | 39.2.3 |
| ClientHello | 32 条，全部独立解析成功 |
| SNI | 8 类；其中 `antigravity-unleash.goog` 13 条 |
| HTTP 阶段 | 未出现；report 只有 `official-app-startup` phase |
| cloudcode | **没有 `cloudcode-pa.googleapis.com`** |
| 生图 | 未验证 |

所以这次补齐的是 Linux 官方 App/子进程启动期间的 TLS 证据，仍然没有官方 LS 的 cloudcode 生图 profile。缺少 HTTP phase 是采集结果的事实，不应解释成“已登录状态无 cloudcode 请求”。

## 控制面结果

`antigravity-unleash.goog` 的 13 条样本字段全部稳定：

- JA3：`03117a8ed39ef02427ebbc39f121275c`；
- cipher suites、扩展顺序、ALPN、supported versions、groups、key shares 均与 TokenKey 当前 CLI canonical 一致；
- ALPN：`h2,http/1.1`；key shares：`4588,29`；supported versions：`772,771`；
- 无 GREASE；
- 签名算法列表为 `[2052,1027,2055,2053,2054,1025,1281,1537,1283,1539]`。

与 `tk_canonical_antigravity_cli` 对比，唯一字段差异是 canonical 中额外包含 `[2308,2309,2310]`。JA3 不包含 signature_algorithms，因此 JA3 相同不代表 ClientHello 完全相同。这个差异说明客户端版本／平台／构建可能影响签名算法扩展，不能只用 JA3 作为 profile owner。

其他 SNI 不能混入 Antigravity 模型 profile：更新服务和 `redirector.gvt1.com` 出现 GREASE 与扩展顺序变化；Microsoft telemetry 也使用不同 ClientHello；Playwright 下载域名恰好复用了控制面样本形状，但不构成 LS cloudcode 证据。

## 与现有证据的关系

本机 macOS 2.15.1 的控制面样本同为 JA3 `03117a8ed39ef02427ebbc39f121275c`，并包含 13 个 signature algorithms。Linux 1.23.2 样本保持同一主体 TLS profile，但少了三项签名算法。这支持“控制面 Go TLS 路线高度共用”的判断，也说明跨版本／平台不能盲目复用所有数组。

这次不更新 `tk_canonical_antigravity_ide_cloudcode`，原因有三点：

1. 没有 cloudcode SNI；
2. 没有 LS HTTP metadata 或生图请求；
3. report 只记录了 App startup phase，无法把 13 条 `antigravity-unleash.goog` 连接归因到某一个 LS 模型请求。

现有 cloudcode JSON 已被标记为历史未独立复核；本次 Linux 样本也不能为它恢复证据等级。最合理的下一步仍是让同事在**已登录 App 的正常运行状态**下，用透明 TCP/TUN 方式捕获目标 cloudcode 连接，或让采集器真正完成 LS HTTP phase；至少要看到 `cloudcode-pa.googleapis.com`，再谈更新 profile。

## 对 TokenKey 的实际建议

- 可以把这次结果作为“官方 Linux 控制面交叉证据”，支持保留当前 Go 风格 `h2,http/1.1` 控制面 profile。
- 不应因为 Linux App 的 App 版本 `1.107.0` 或 Electron 39.2.3 去改 macOS Hub UA、Chrome 模拟版本或 cloudcode TLS。
- 不应把 Linux 的 signature algorithms 直接覆盖 macOS／CLI canonical；应按目标 SNI、LS 版本和平台分别保存 evidence，只有重复的目标 cloudcode 样本才可提升 canonical。
- Manager Chrome123 仍是独立模拟组；本次官方 Linux 证据没有支持 Chrome123 更接近官方 LS 的说法。

验证：ZIP 完整性检查通过；32 个 raw ClientHello 由仓库 parser 独立解析；SNI 分组和稳定性重新计算；与 `tk_canonical_antigravity_cli` 的字段逐项比较完成。未进行 Google 请求、账号验证、模型调用或部署。
