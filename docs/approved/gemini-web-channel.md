---
status: approved
approved_by: user
source_baseline: 75e38806d7f04202f3ee02148f10975b7d26871c
pr_base: 6cfe353d765cf0f2868fcf56d1f22475e79785d3
implementation_approval: user-explicit-database-only-admin-import-and-copy-initialization
observed_on: 2026-09-21
target: edge-us4
---

# Gemini Web Cookie HTTP 通路

## 当前授权与边界

PR #2262 提供 Cookie＋纯 HTTP Worker，沿用 Gemini API-key 账号与官方
GenerateContentResponse 格式。用户明确要求删除文件模式，控制接口沿用现有 edge
鉴权规范，并修复复审发现的问题。本文件定义当前实现契约；旧浏览器迁移、文件 canary
操作步骤不再是实施指令，历史调查保存在
[先前版本](https://github.com/youxuanxue/sub2api/blob/f48c07d3fb4799392ebe0b113238cd2d43a61267/docs/approved/gemini-web-channel.md)。

导入实现阶段不部署、不调用真实 Google、不启用商业定价。用户已确认在编辑账号中导入浏览器
JSON，并要求消除并发、适用边界与凭证泄露风险。身份去重与保存时 bootstrap 验证
不属于完成声明；导入成功仅证明持久化成功。API Key 输入框不能直接粘贴 Cookie。

## 兼容协议推进计划（本轮会话授权）

用户已确认区分通用 Gemini 与 Gemini Web，要求 Messages、Chat Completions、Responses
复用 converter → 原生 generateContent，并明确指挥 Agent 并行实现。该授权覆盖本地实现、
测试与复审；合并、部署和生产验收是后续独立步骤。本节取代下文原先“兼容入口一律不承接”的
阶段性限制；未完成验证不代表已部署。

### 复审结论与契约

Gemini 是协议，Gemini Web 是受限供应源。传输 profile 负责原生端点和认证，能力 profile
负责可接受的请求；不把 Web 限制施加到普通 Gemini。原始请求先由 `protocolrouter.Plan`
判定，选中账号和 Plan 决定执行。不能在 converter 中压平历史、删除 system/tools，或者
仅凭账号名、域名和模型别名判断 Web。无能力的候选退出当前请求的竞争，仍在原授权范围内
寻找其他兼容账号；不放宽授权、收费组或供应源凭据检查。

兼容协议统一使用 `generationConfig` 扩展传入 `responseModalities` 和 `imageConfig`，
保留显式 null 和合法比例。三个入口复用现有 Gemini 转换及传输，不新增独立 Web 网关。
回包复用经校验的图片 markdown data URI 文本投影，各协议保留合法 JSON/SSE 外壳；
这一契约保证图片数据可获取，不保证所有客户端都会自动渲染。图片计数来自实际有效输出，
文本响应或错误不能因模型名含 image 而计作一张图片。

缓冲流沿用 Worker 现有语义：完成生成及下载后返回 SSE，不承诺首 token 延迟。
断流不伪造成功，已经输出的内容不自动重放。

**本轮补充批准：Web 输出上限采用 best-effort。** 用户明确选择“允许 Web best-effort：
明确不保证 token 上限”。Messages 仍须提供合法正整数 `max_tokens`；Chat 的 `max_tokens` /
`max_completion_tokens`、Responses 的 `max_output_tokens` 和原生 Gemini 的
`generationConfig.maxOutputTokens` 具有同样的 Web 限制：网关不保证上游执行该上限。

这项降级仅由 Plan 对显式 Web 供应源处理：验证原始请求，只从不可变 effective request
移除对应输出预算，保留原始正文与 digest，并记录 `gemini_web_best_effort_token_limit`
调整原因。converter 不再猜供应源能力。既有兼容等级排序优先有能力保持原请求的账号；
需要 Web 承接时仍可选到合法的 best-effort Plan。普通 Gemini 原样保留预算；不删除 system、
历史、tools、采样配置或图片输入。缺省预算不制造调整。原生 Worker 的严格 HTTP 契约不变，
该 best-effort 发生在统一网关 Plan，随后发送它本来支持的原生请求。

### 分工、依赖与交付门槛

| 阶段 | 执行分工 | 完成条件 |
|---|---|---|
| 能力与 Plan | capability Agent | 复用现有 native Web 校验，覆盖四种入口、原始语义和兼容候选回退；普通 Gemini 回归通过 |
| 转换与输出 | conversion Agent，可与能力工作并行 | 共享扩展透传、缺省上限保真、图片 JSON/SSE 输出与实际计数；错误及多图断言通过 |
| 执行与集成 | execution Agent，依赖前两项接口 | 实际 converter + HTTP transport 验证 native path、认证、模型映射、参数和返回图片 |
| 收敛与复审 | 主 Agent | 合并各项改动，更新 owner/sentinel/Story，运行聚焦测试、preflight，再按 Jobs 标准审查完整用户旅程 |
| 发布与验收 | 主 Agent，取得发布授权后 | 部署可回滚版本，按下述单账号 canary 核对 HTTP、图片解码、账号和 usage，完成清理 |

当前聚焦兼容生图与单轮文本。工具、多轮、多模态输入、会话管理重构、模型目录与定价
变更不属于本次实现；这些工作不能靠静默删字段混进单轮成功路径。

### 生产 canary 的执行契约

复用 `ops/observability/run-probe.sh` 与保留调试资源的 account-model probe，目标由实时
账号快照确认，不能凭名称推断能力。预先核对请求授权路径的图片权限，以及 Messages 转换所需
的 `AllowMessagesDispatch`；探测不自动修改业务组开关。先测试文本控制，再逐项调用生图；每次付费生成串行，
不以失败后盲重试获取绿灯。之前的 HTTP 400 仅证明网关拒绝，不能证明上游缺少生图能力。

请求只含一个 user 文本，不注入探测用 system/instructions；Chat/Responses 缺省输出上限，
Messages 提供合法 `max_tokens`，并核对 Web Plan 的 best-effort 调整。覆盖默认图片配置、显式 null、4:3；先做非流式，
再验证约定的缓冲 SSE。判定必须包括 HTTP 成功、图片 base64/MIME/实际解码、非空像素、
服务端响应 request ID、usage 的实际账号归属；HTTP 200 或有 image 模型名均不足以通过。
单轮限制负向请求必须在上游副作用前拒绝或转交原授权池的合法候选。最终执行调试资源
cleanup dry-run，并记录缺口。未部署时本地 HTTP fixture 测试仅称集成测试。

## 数据与会话 SSOT

唯一可执行会话为目标 edge 的 `accounts.credentials.gemini_web.runtime`：

```json
{
  "version": 1,
  "user_agent": "<同一浏览器的真实 UA>",
  "cookies": [{"name": "<name>", "value": "<secret>", "domain": ".google.com", "path": "/"}],
  "state": {"blocked": false, "generation_pending": false, "last_refresh": 0, "cooldown_until": 0}
}
```

UA 只有 `user_agent` 一种字段名。Cookie 保留域、路径、过期时间和 CDP 属性；
Worker 以当前 jar 为准保存，已删除或过期的 Cookie 不得从导出元数据复活。
不保存第二份可执行 source bundle，不读写账号清单、会话或状态文件。

普通 DTO、审计日志共享 SensitiveCredentialKeys 脱敏清单，整个 `gemini_web`
不回显。编辑未提供该字段时保留已有凭据；复制账号保留空的 `gemini_web` 能力声明，
清除其中全部会话、状态及租约，副本保持不可调度。声明与会话不混淆：
runtime 是否存在仍是会话绑定 SSOT，不增加 extra 初始化标志。

## 控制接口与执行

控制路由挂在 `/api/v1/edge/gemini-web`，复用现有 edge API-key 和活跃管理员 owner
中间件，不新增 Gemini 专用私网限制或后端服务令牌。传输与部署遵循现有 edge API 规范。

- `GET /accounts/:id/session`：读取该 active、schedulable 的 Gemini API-key 账号 runtime 和 Worker key。
- `PUT /accounts/:id/runtime`：以 expected_version 和租约 owner 比较交换 runtime，服务端递增版本。
- `POST /accounts/:id/lease`、`DELETE /accounts/:id/lease`：取得/释放数据库账号租约。
- `GET /warm-accounts`：返回到期账号的整数 ID 和 `protocol_version=1`，不批量返回 Cookie。

Messages 与 native 两条网关入口都从实际选中账号注入 account ID；
Worker 校验该账号的 API key，不能仅凭 account ID 执行。维护任务使用独立内部调用路径，
不以空 key 假装通过鉴权。

Admin 的“测试账号”复用同一 account ID header owner，绑定本地 runtime 的账号才注入；
prod 中继不携带本地 ID。测试请求按显式 Worker/relay 声明生成单轮文本或
TEXT+IMAGE，不附加 Worker 不支持的 systemInstruction；生图可传入契约允许的
imageConfig.aspectRatio。
普通 Gemini 测试保持原请求形状；用户实际请求的角色、参数不作删减。
此修复不绕过控制接口既有 active/schedulable 检查，也不自动恢复错误或开启调度。

每个账号只有一个 SessionOwner。生成、后台续期、读取最新版本、替换内存和保存共用操作锁。
跨进程租约和 runtime CAS 在同一数据库账号行执行；租约 600 秒，Worker 总操作预算 480 秒，
每次上游请求 timeout 不超过剩余预算。到期禁止新请求与写回；竞争/版本冲突丢弃本地 owner。
释放只删除匹配 owner 的租约。故障后不自动重试生成。

Worker 每分钟只读维护元数据，只有到期账号才申请租约并执行 RotateCookies → bootstrap，
正常续期间隔十分钟；暂停、冷却、在途租约和未确认生成不会进入维护列表。
每次 HTTP 响应保存 Cookie 更新；runtime 未变化时不重复写入。
生成前持久化 generation_pending，完整成功或明确 quota 拒绝后清除；本次调用收到上游
明确鉴权拒绝且已将会话标记 blocked 时，也清除 pending，但 blocked 继续阻止请求。
本地暂停返回的 403 不清除 pending，连续重试不能绕过暂停。
下载失败或未知生成结果跨重启保持暂停，防止重复消耗额度。

普通账号编辑和 credentials 更新在数据库行锁内保留当前会话，防止旧快照覆盖 Worker 更新。
导入必须显式使用当前版本加一；持有在途租约时拒绝导入，不能通过全量 credentials 覆盖绕开并发保护。
后台导入复用此数据库 owner 的专用原子更新，不走通用全量账号更新。

## Admin UI 会话导入

`POST /api/v1/admin/accounts/:id/gemini-web-session` 复用管理员鉴权。仅允许声明了
`credentials.gemini_web` 对象的本地 Gemini API-key Worker 账号；复制账号保留空对象，
因此同一个入口可执行首次初始化（无 runtime，版本从 1 开始）或替换（按当前版本 CAS）。
初始化只接受已关闭调度的账号；SQL 重验，若并发编辑启用调度则返回 409。
`gemini_web_relay` 或 `extra.relay_kind=gemini_web` 的 prod 中继、普通 API Key 均不可导入。
UI 用 `has_gemini_web` 显示资格、用 `has_gemini_web_runtime` 区分初始化和替换，Handler
与 SQL 再验证边界。不会自动向 prod 中继或其他 edge 转发 Cookie。

- 前端读取文件前检查 2 MiB 限制，后端独立限制请求体；校验格式、UA、Cookie
  数量、域、字段类型与长度。域写入前规范化，缺少 path 使用 `/`；CDP 的
  `expires=-1` 会话 Cookie 和小数时间戳合法，其他 CDP 元数据原样保留。
- 显式导入根据读取的 expected version 执行单条 SQL CAS（内部 -1 代表 runtime 不存在），重新检查声明、绑定、版本及
  租约；只有实际更新一行才成功。竞争导入、Worker 更新或活跃租约返回 409，
  不返回成功摘要，不自动重试覆盖更新。普通编辑保留当前 runtime 的合并语义
  不用于判定显式导入成功。
- SQL 仅替换 runtime、删除过期 lease 并更新 updated_at，不修改其他凭据、模型映射、
  status、schedulable 或 extra。新 runtime 清除 blocked/pending/cooldown 并重置
  last_refresh，后续实际 Worker 请求仍执行认证与 bootstrap。
- 初始化成功保持 `schedulable=false`，运营确认后再手动启用调度。成功摘要是本次已提交版本的回执，不是随后 GET 可能读到的更新版本；账号 DTO
  沿用凭据脱敏。审计整体省略导入请求体。文件解析/读取/API 错误统一显示固定
  文案；409 提示稍后重试，禁止将原始错误中的凭据片段带入 UI。
- 选文件时固定目标 ID；关闭、卸载或换账号使旧操作失效。尚未发送的操作不提交；
  已发送请求仍只作用于原账号，迟到响应不更新新弹窗。同一账号对象刷新不清空摘要。
  导入响应和状态刷新不覆盖编辑中的未保存输入；重新打开、切换账号或账号类型时才回填表单。

运营流程为：复制本地 Worker 账号 → 编辑副本 → 选择凭证 JSON 完成初始化 →
核对导入回执 → 手动启用调度。响应 `session.mode` 为 `initialized` 或 `replaced`；
替换保留原调度状态。一个 edge Worker 继续按账号 ID 隔离会话，无须为副本新建进程。
升级前已复制且缺少声明的账号不能按名称或分组自动识别；需重新复制真实 Worker 账号。
本轮不迁移旧副本、不修改线上数据。未初始化账号即使误开调度，候选准入也拒绝承接请求。

前端 Playwright 使用真实编辑页面和文件输入、模拟后台响应；数据库并发另用真实
PostgreSQL 验证。两者均不代表线上部署或 Google 会话有效性验证。

普通账号编辑复用已有行锁查询判断会话绑定，无绑定的普通 Gemini 账号不执行额外会话查询。
Worker 启动校验控制协议；只读 `--check` 检查账号绑定、密钥、runtime 和 concurrency=1，
不申请租约或请求 Google，不能证明 Google 会话仍有效。控制请求拒绝重定向以避免转发管理员 key；
该 key 仍继承现有 edge 管理权限，本轮不新增 Gemini 专用权限或网络规则。

Worker 保留健康请求容量，生成最多四并发、图片操作最多一并发，过载返回 503 和 Retry-After。
`/healthz` 表示进程存活；`/readyz` 要求最近控制检查成功且未排空，Docker 健康检查使用后者。
SIGTERM 停止接收新任务并等待在途操作释放租约；异常崩溃仍沿用租约到期和未知生成暂停保护。

## 请求与图片契约

Worker 支持 `gemini-web-flash`、`gemini-web-pro`、`gemini-web-pro-image`，
表示 Web 类别而非官方付费 API 型号。只接受单轮文本和 responseModalities；
生图可额外接受 `generationConfig.imageConfig.aspectRatio` 的 `1:1`、`9:16`、`3:4`、
`4:3`、`16:9`，并映射到网页 RPC 已实测的比例字段。其他 controls、tools、system、
多模态输入和多轮历史在 Google 副作用前拒绝。生图请求省略 imageConfig 或传入
`imageConfig: null` 均表示未指定图片配置，沿用默认生图 RPC。非 null 的 imageConfig
必须为仅含 aspectRatio 的对象，且比例为上述字符串；错误类型和额外字段在写会话或
请求 Google 前返回 400，与 Go 候选准入保持一致。文本模型不接受 imageConfig。
streamGenerateContent 返回生成及下载完成后的一条 SSE，不声明首 token 流式延迟。

Worker 输入契约保持不变；统一网关仅执行上文经用户批准的输出预算 best-effort。共享候选准入在计费、
选槽及等待后刷新时排除 Worker 不支持的请求，继续在原授权范围选择其他兼容账号。
edge 以 `credentials.gemini_web` 会话对象识别此能力边界；专用 prod 中继显式声明
`credentials.gemini_web_relay=true`，不根据账号名、域名或公开模型别名推断。
Gemini API-key 账号的 native 请求与兼容请求统一进入 Plan。现有账号启动准备与新账号
生命周期通过已有 capability owner 创建原生协议记录，以 `native_declaration` 区分平台
声明和实际探测；不伪造探测成功，也不推断 Chat/Responses 上游端点。运行时只读关联记录，
不按平台临时合成 supported_protocols；缺少链接必须拒绝并修复账号生命周期。
已有链接必须继续通过身份、版本与证据校验；空能力、缺半边链接、冲突或陈旧证据不能被重新播种覆盖。
Web 能力声明参加端点身份，变化后须重验 Plan。普通 Gemini 不套用 Web 的单轮限制。
原生单轮文本与生图保留（generateContent / streamGenerateContent），包括上述五种比例；原生 countTokens
不由 Worker 承接，继续选择其他授权兼容账号。Chat/Responses 转换保留客户端未指定上限的语义，
不再补入人工 maxOutputTokens。显式输出预算按本轮已批准的 Web best-effort 契约由 Plan
调整并审计；普通 Gemini 保留预算。
Anthropic `count_tokens` 继续复用网关本地估算，不受 Worker 生成能力限制。
调度投影与 Worker 共享请求测试样本，防止准入约束漂移。上线需要部署后端并给
prod 中继补入声明；映射、Cookie、并发和调度开关不随代码修复变更。 发布流程在目标镜像包含该准入逻辑时、
任何 prod 变更前执行 `probe-gemini-web-relay-declarations.sh`：按显式
`extra.relay_kind=gemini_web` 核对布尔能力声明，也检查错误类型的声明；包含关闭调度的账号。
声明缺失或读取失败阻止部署；旧版回滚目标不消费此声明，跳过该检查。检查只读取脱敏投影，
不根据账号名、URL、模型映射推断能力，也不自动启用账号或修改凭据。

图片必须经 original RPC 和受限域名下载，实际解码并核对 MIME；不返回预览、不放大、
不因下载失败重新生成。探测同样要求完整解码和正确账号用量归属。
解码前拒绝超过 1600 万像素的图片；图片准入锁覆盖下载、解码及响应缓冲。
不虚构 usageMetadata、modelVersion 或返回内部 thoughts。

2026-09-24 在已登录的 11 号 AdsPower Gemini 页面逐项选择五种比例并抓取真实
`StreamGenerate` 请求：比例字符串位于 `inner[0][9][6][1][1]`，枚举值位于
`inner[55][0][0]`，映射为 `1:1→61`、`9:16→59`、`3:4→60`、`4:3→62`、
`16:9→63`。`4:3` 返回 HTTP 200 和 1024×765，`9:16` 返回 HTTP 200 和
572×1024。该证据仅验证网页协议映射；线上 TokenKey Worker 仍需部署后再做账号归属
和 Cookie 会话验证。

同日以 11 号浏览器 Pro 生图实测未指定比例与显式 null：基准请求的 `inner[0]`
无索引 9 图片选项、`inner[55]=[]`；另一请求设置 `inner[0][9]=null`、
`inner[55]=null`。两者均返回 HTTP 200 和 1024×559 图片。因此本适配器将生图
`imageConfig: null` 解释为未指定配置；该证据来自 Web 位置数组 RPC，不代表官方
付费 API 对同名 JSON 字段的验证结果。

## Implementation / Owners

| 行为 | 唯一 owner |
|---|---|
| 浏览器导出与 Worker 共用 Cookie 域范围；Go 为测试约束的投影 | ops/gemini-web/session_contract.py；Go/Python 双向契约测试 |
| 管理后台会话导入校验与回执 | backend/internal/handler/admin/account_handler_gemini_web_import.go |
| 本地 Worker 导入资格 | backend/internal/service/gemini_web_request_tk.go 的 CanImportGeminiWebSession；SQL 写入重验 |
| 文件选择、大小限制与弹窗异步生命周期 | frontend/src/components/account/EditAccountModal.vue |
| 会话生命周期及 HTTP 协议执行 | ops/gemini-web/worker.py 的 SessionOwner / Account |
| edge 控制请求 | backend/internal/handler/gemini_web_session_handler.go |
| 数据库租约与 runtime CAS | backend/internal/repository/account_gemini_web_tk.go |
| 网关账号引用 | backend/internal/service/gemini_web_tk.go |
| 凭据脱敏与留空保留 | backend/internal/service/account_credentials_redact.go |
| 复制账号重置 | backend/internal/service/admin_account.go |
| 探测结果判定 | ops/stage0/probe_account_model_verdict.py |

DI、路由鉴权和关键调用/测试锚点由 scripts/sentinels/handler-di-wire.json 防回退。
运行命令只放 [Worker README](../../ops/gemini-web/README.md)，验收映射只放
[US-056](../../.testing/user-stories/stories/US-056-gemini-web-http.md)。

## 历史在线证据（不证明本次数据库实现已部署）

2026-09-21 06:41–06:43 UTC 的旧 canary 经 prod #200 / us4 #28：
图片 HTTP 200、JPEG 2816×1536、2,778,476 bytes；
SHA-256 `9c3ae23fde616a093502ac17d391ef3d09d99c50469f9ed8afac1fbbf3d09559`。
prod request ID `3daa3f2c-9616-43c6-b813-489542e29a82`，edge request ID
`39c15fb6-78c7-4fba-842c-fe12e8c4168e`。文本返回 `TK_133_RESTORED_OK`。
当时 #29 / profile 483 仍暂停；不据此声称双号持续健康或长期 Cookie 有效。
