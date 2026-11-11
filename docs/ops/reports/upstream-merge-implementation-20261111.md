# 2026-11-11 上游合并实现与验证记录

状态：隔离分支完成实现和本地回归；最终 preflight、提交后的审计和 CI 结果见 PR「验证」。未合并主线或部署。

固定基线与授权范围见 [合并范围](../../approved/upstream-merge-2026-11-11.md)。最初 upstream harness fork base 为 `8a375b68b6cc107cbc36ea086a20b2fbdb52d6a0`，当前 PR 比较基线为 `87e6007a496d273d92c247a88fb134ae73761d76`，upstream target 为 `530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484`。用户授权提交推送和独立 runtime 合并 PR；#3132 保留为前置审计。生产迁移、主线合并和部署仍需另行确认。

## 重点风险核对

| 风险 | 代码裁决及实际影响 | 验证状态 |
|---|---|---|
| upstream 覆盖 TokenKey 共享 owner | 保留候选/Plan、客户计费、QA raw、UseKeyGuide、UsageCell dispatcher、PaymentPanel 和渠道表单转换 owner；不复活被删除的旧架构 | 前端 sentinel 通过；最终结果见 PR preflight/CI |
| 缓存写计费混用 TTL | OpenAI 30m/12h 与 Anthropic 5m/1h 独立落库；Anthropic 未知 TTL 继续按 1h；NULL 与显式零的价卡语义分开 | JSON、SSE、透传 JSON/SSE 四条路径均验证实际 RecordUsage 扣费及 preview 一致；真实 PG round-trip 通过 |
| 订阅长上下文误收费/误免除自定义价 | OAuth/SetupToken 的实际 Sol/Terra 上游模型只豁免模型价卡附加费；管理员区间价保留；预览、扣费、响应模型重定价及账号成本镜像共享该标记 | 恢复此前 parked 测试；订阅豁免、管理员区间价、响应模型重定价、账号成本镜像定向回归通过 |
| 分区 usage 表覆盖索引建库失败 | 249 索引迁入既有分区 concurrent child + parent attach owner，父子均保留 INCLUDE；250 仅添加列 | 新增真实 PG 的父子索引、重试、后续新分区和求和验证。真实 PostgreSQL 18.1 测试通过：覆盖索引、重试、新分区、求和及 usage/channel TTL round-trip |
| 读超时误记 499 | 413 优先；typed read timeout 保留 408，即使 net/http 同时取消上下文；真实调用方取消仍消费 canonical 499 owner | 对应 HTTP/1、HTTP/2、断开及截断 body 测试保留；完整 unit 通过 |
| 单模型临时错误扩大到全账号 | 保留模型级 capacity/500 冷却、软饱和度与 Retry-After；显式规则 scope、401 凭据优先级与状态元数据补齐 | 恢复 parked 测试并修复 scope 未解析及 HTTP capacity 分支丢失默认模型策略；定向回归通过。另补 Gemini 最终策略的 mapped model 传递，完整 unit 通过 |
| WS 后续轮 failover 重放旧请求 | 保留当前轮 payload wrapper；可安全重建当前轮时允许 failover，无安全 payload 或跨账号 continuation 所有权不可读时 fail closed；首轮语义保留 | 首轮/后续轮组合回归通过，替换上游收到当前轮 input、tools、instructions，而非重放首包。此前生产采集未观察到 WS usage，不能据此证明无风险 |
| 工具图片归属回退 | 守卫抓到上游重新把图片放在 sibling parts；恢复每个 functionResponse 自己携带图片，避免多工具错配 | 原有归属断言继续保留。此处有意保留 TokenKey 对 legacy model ID 的嵌套行为，未采用上游 sibling fallback |
| Codex 接入配置变化 | 标准 auth.json 仍为默认；API-key native image 模式可选；动态模型目录继续决定可选项 | modal/共享 guide 定向测试通过；桌面和移动端真实 Playwright 共 8 场景通过 |
| 管理页面刷新丢状态 | 旧额度快照在被动批量刷新时保留，loading 由共享 owner 传递；主动查询禁重复；Key 列表独立展示统计 loading | 账号/Key 定向 112 用例通过；账号窗口、设置、模型映射真实 UI 6 场景通过 |
| refresh token 上游安全行为差异 | cache consume 能力保留；不替换 TokenKey 既有并发 refresh 短宽限期。宽限期重放窗口仍存在，不能宣称已获得上游严格单次消费保护 | 保留原有 grace/disabled/password-version 回归；并发旧快照按短宽限期验收，定向通过 |

## 线上证据边界

前置审计采集到 prod/cn-prod 运行 1.8.359 / `77f6f1497`，没有 30m/12h 新 schema，且存在实际 429/503 压力与缓存写入流量。这证明计费和冷却变化会触达真实路径，不证明新实现已经在线有效。本次没有对生产执行 schema、配置、账号状态或流量写入；没有用新增模型元数据代替线上供应池激活证据。

上线正面作用预计来自：缓存写 TTL 更准确、Key 成本趋势覆盖索引、typed body timeout 诊断、后续轮 WS 防错误重放、新图片选项，以及保留旧数据的额度刷新。上述效果仍取决于验收通过和之后的实际发布。

图片缓存费用存在明确变化：此前 registry→计费结构漏传专用价，回退为文本 cache-read 价；修复后 GPT Image 2.6 的相关缓存项按已发布 2e-6/token，而非文本 1.25e-6/token 结算。混合缓存定向测试验证实际费用；这会增加相关请求的缓存项扣费，不代表总账单同比例增加，也未修改 registry 定价。

负面成本和残余风险：新增列/索引的 DDL 锁等待与 I/O、价卡元数据及模型兼容面扩大、refresh grace 安全取舍、legacy 工具媒体转换分叉，以及生产未覆盖的 WS 路径。当前不足以给出可上线结论。

## 实际验证证据

本地日志以 `/tmp/tk-upstream-20261111-` 为前缀；下列完整 unit 结果对应最初 harness `8aa25133f`。后续 main 对齐后的完整重跑及最终 preflight 结果见 PR「验证」，不把旧树的通过冒充新树证明。

- 完整 repository integration 通过（44.533s，`integration-main-final.log`）：真实 PostgreSQL 18.1，覆盖分区 concurrent 索引重试、父子 INCLUDE、新分区、usage/channel TTL round-trip，以及实际 SQL 对旧记录与旧写入的兼容。
- 前端账号刷新/Key 列表等定向 112/112，价卡/共享指南/白名单等定向 216/216；lint 和包含 vue-tsc 的 production build 通过。对齐后的新增 main 改动不涉及前端，release asset 检查通过。
- Playwright 真实 Chrome 14/14：公开接入及 Codex 模式桌面/移动端 8 个场景，账号窗口、设置和模型映射 6 个场景。入口为 `E2E_USE_SYSTEM_CHROME=1 pnpm -C frontend exec playwright test --config playwright.upstream.config.ts`；使用隔离 fixture，不请求付费上游。
- Ent 重新生成成功；未改 Wire owner。Gateway semantic sentinel 通过，新增锚点由完整 preflight 及 PR CI 覆盖。
- 定向 service 回归通过（7.657s，`regressions-frozen-v3.log`）：图片缓存费用及价格缩放、模型级 overload、count_tokens 实际模型隔离、隐私 auth snapshot、refresh grace、超期 refresh 零持久化、流式 keepalive 和大帧 WS。
- main #3150 的 QA 归档测试通过（10/10，`qa-main3150.log`）。
- **完整 backend unit：通过**（`unit-frozen-v2.log`）。
- backend lint 已通过；完整 preflight 首轮仅因 upstream fetch 的网络错误失败。代理链路已验证可用，完整重跑结果见 PR「验证」。

## 接缝修复与断言迁移

- Responses `output_text` 保留真实 annotations/logprobs，只对缺省值补空数组；重复 reasoning helper 的 summary/raw fallback 已由既有 owner 承接，移除未接入的重复实现。
- 工具媒体仍嵌在各自 functionResponse，包含 legacy model ID，不接受 sibling parts 造成的多工具归属错配；原有 TokenKey 断言保留。
- WS 安全重建场景验证后续轮实际 input/tools/instructions，无安全 payload 或不可读的跨账号 continuation 仍 fail closed；首包不重放。
- GLM 5.3 的 none/minimal 先按既有供应方 owner 规范为 low，GLM 5.2 保留 none/minimal；策略 deny/ceiling 继续验证最终协议请求。
- 生图保留 OAuth stream+n>1 拒绝及默认省略外层 reasoning.effort；单图继续覆盖 JSON-to-SSE，非流式保留批量正向，流式批量验证零上游调用。新图片缓存价格沿用已发布 registry，修复 registry→计费结构和价格缩放漏传，实际验证混合文本/图片缓存费用，不新增价格真值。
- 5.2 新型号的 legacy/catalog 行为使用显式 fixture；生产构造器缺少已发布 owner 时 fail closed。Sonnet 5.5 cache-read 保留现有 TokenKey 价格。远端 catalog/hash 不同步仍重试下载，但 sensor 不得自动发布计费价格。
- 模型 overload 命中后保留 RequestScopedTransient，关闭同账号重试，防止外层升级账号惩罚。Antigravity 图片 INTERNAL 500 环境变量接入实际模型冷却 owner；文本保留既有时长，两者均验证零账号惩罚。
- count_tokens 只在真实支持的官方 API-key fixture 上验证上游错误；foreign relay 继续本地估算。四条计数路径传递实际 mapped model；Anthropic failover 提前返回前仍执行显式模型级 401 冷却，并禁止同账号重试及账号惩罚。
- 分组平台解析与隐私 gate 复用同一已认证、hydrated 的 ctx snapshot；legacy/advanced 均验证正确隐私选号和零分组数据库读取。
- setup-token 显式/强制/批量查询复用既有 passive 周额度 owner；非 Anthropic setup-token 保留估算。自定义错误码排除的普通状态不得停用账号，凭据及独立健康熔断优先级保留。
- refresh 继续采用 TokenKey 短宽限期，并发旧快照在同一用户/family 内重新签发；不采用上游严格单次消费测试预期，不能据此声称已消除宽限窗口内重放。过期、禁用和改密的原有安全回归保留。
- 定时测试改为观察真实事件：keepalive 等待实际 ping 后结束上游；超期 refresh 等 ctx 真正过期后返回。大帧 WS 测试的预算覆盖负载较高机器处理 17 MiB 请求所需时间，不改变运行时代码或帧限制断言。
- repository 集成的统计污染来自 upstream-400 streak fixture 未清理成功记录；按测试用户清理，session-id fixture 使用回滚事务，统计断言不变。
- 恢复 upstream parked 的 scope 与订阅长上下文测试，由 unit runner 实际收集；没有通过新增 skip/only 逃避验收。功能/资产删除仅为 upstream 替换的合作方 logo；两份 deferred 测试为路径迁移，已退役的 EDGE_SECURITY 不复活。

## 线上只读迁移核对

2026-11-11 12:27:39 UTC（20:27:39 +08:00），通过数据机入口读取 prod/cn-prod：PostgreSQL 均为 18.6；usage_logs 分别 9,720,552 / 1,123,192 行；两站均无新增 TTL 列，采集瞬间无等待锁。探针为 READ ONLY 事务，statement_timeout=10s。线上默认 lock_timeout=0，迁移 250 因此增加 SET LOCAL lock_timeout=5s、statement_timeout=1min。

250 使用稳定常量 DEFAULT 0，在该 PostgreSQL 版本采用元数据缺省值；真实 PG 验证原记录读到 0、旧应用省略新列仍可写入。父子表仍需短暂取得锁，超时整个事务回滚。249 的索引 I/O 与磁盘成本由既有逐分区 concurrent owner 承担。应用回滚保留新增列/索引，不执行收缩 DDL；没有在生产执行迁移，不宣称锁成本为零。

## 主线与指纹边界

原审计基线为 `77f6f14977222171b5aba41ae99b8d078f01c42c`。按用户要求，先对齐到 main `8a375b68b`，保留 #3149 Agent Plan 视频内容根路径与供应源探针修复，以及 #3150 QA 暂存目录清理；最初 harness 保留固定 main 和 upstream 的两个 parent。提交后 main 又推进至 `87e6007a4`，额外 merge 完整纳入 #3151 Chat 可保留性 memo、#3152 Kiro 方案 B 计费归因、#3153 newapi OpenAI image edits 和版本 1.8.363；这些 main-only 代码与新主线逐文件比较无差异，image builder 薄挂点自动合并后继续保留两侧行为。未提交的解冲突结果均保存为本地 Git tree/补丁，最终审计以提交后的 HEAD 为准。

Claude Code 官方 2.1.322 darwin-arm64 工件按官方 manifest SHA256 校验（`a9d237949db816a0b7afd88a9a8e1df36b3a597a30f5a41d1c0539eb180a3450`），安装后真实版本为 2.1.322；版本 owner 经同步脚本生成副本，静态检查与 StaticVersionTests 2/2 通过。未以版本号推断 TLS/beta/Stainless/system 参数变化。model-surface bundle 只同步元数据与 floor hash，不激活生产模型供应或价格。

## 验收边界

早期 full unit/preflight 的文件快照失效、共享 Go 缓存缺失及实际失败均不算通过；后续使用独立 Go 缓存、固定源码重新验证。完整 unit 已通过（service 376.747s）；提交后的 ancestry audit、最终 preflight 和 PR CI 独立记录在 PR 中。本地通过不替代生产迁移、主线合并及部署授权。
