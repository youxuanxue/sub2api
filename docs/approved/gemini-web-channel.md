---
status: approved
approved_by: user
source_baseline: 75e38806d7f04202f3ee02148f10975b7d26871c
pr_base: 6cfe353d765cf0f2868fcf56d1f22475e79785d3
implementation_approval: user-explicit-database-only-admin-import-and-copy-initialization
observed_on: 2026-09-21
revised: 2026-11-20（再压授权散文/AdsPower 抓包像素；保留契约与 Owners）
---

# Gemini Web Cookie HTTP 通路

## Nano Banana Pro（直接 Web Pro）

旧内部模型 `gemini-web-pro-image` 保持 Nano Banana 2 / Flash 行为。本节覆盖直接 Pro 生图与精确画布后处理。


- 公共模型 `gemini-3-pro-image`，别名 `nano-banana-pro`、兼容别名 `nano-pro`。
  不新增容易与 Nano Banana 2 混淆的 `nano-2-pro`。
- 独立 Worker 模型 `gemini-web-nano-banana-pro`：新会话一次生成，Pro selector 加
  `inner[0][9][6][1][0]=1` 和 `inner[32]=1`；比例在同一个 options 中保留。
  最终候选帧 `[42]` 必须严格等于 `Nano Banana Pro`。缺失、未知或 Nano Banana 2
  都不得返回 Pro 成功；沿用 pending 暂停，不重复生成，不伪造 Developer API modelVersion。
- Antigravity 无真 Pro 证据，移除 Pro→Flash 默认/快捷配置；持久化、runtime、通配符
  映射亦不能让 Pro 被 AG 接纳。普通 Gemini/Vertex 真 Pro 供应源不被全局屏蔽。
- 分辨率参数统一 `imageConfig.imageSize=1K/2K/4K`。Web 取得原生约 2K，本地网关
  复用现有精确画布的渐进 CatmullRom / 等比 pad owner 输出目标尺寸；4K 是本地放大，
  不是原生 4K。GPT 现有尺寸限制保持原样，Gemini 使用自己的已定义画布上限。
  不缩放预览图；Google original RPC 和解码校验继续强制。
- 比例对齐共享 Gemini 图片词汇：`1:1,2:3,3:2,3:4,4:3,4:5,5:4,9:16,16:9,21:9`。
  `apicompat.GeminiImageCanvas` 为 ratio/分辨率画布 owner；Worker 只负责网页比例字段。
- 新 Pro 支持单个 user turn 中的文本与 `inlineData` JPEG/PNG/WebP 参考图。
  参考图合计最多 10 MiB、最多四张、单图最多 1600 万像素；base64/MIME/解码检查在
  上传前完成。只上传给已验证的 Google upload host；不获取客户提供的远端图片 URL。
  多参考一致性不作承诺；数量/像素边界由本地测试覆盖。
- 原生 Gemini、Chat、Responses、Messages 共享 Plan；OpenAI Images generations
  复用既有 Gemini 操作适配器，`n=1`、base64 返回，不提供 edits/mask。
  Images 省略 size/auto 不再在通用转换器强塞 2K：AG 已有 wire 默认仍为 2K；Web 默认保留原图。
  `size=1K/2K/4K` 和方形像素别名继续映射到相同 imageSize。
  system/tools/采样控制/多轮请求继续拒绝；不以丢参声称和 AG 完全等价。
- 本地 Worker 账号前的网关消化分辨率并处理原图；远端 Worker 上游保留参数转发，
  不二次放大。输出成功后沿用原结算，按 Pro 价格及交付尺寸档计费；处理失败不结算图片、
  不自动再次生成。流式依旧缓冲，处理后返回同一 SSE 结果。

发布边界：代码合并不等于部署/启用。停用、报错或 pending 账号不随上新恢复。
rollback 先撤 Pro 映射再回滚 Worker。

### 增量 Owners

- 上游身份、引用上传与会话：仍为 `ops/gemini-web/worker.py` 的 Account/SessionOwner。
- Web 请求准入及本地尺寸投影：`protocolrouter/gemini_web.go`；原请求保留交付意图。
  Plan 的参考图转换复用这份严格准入，限定为 Web Pro → Gemini，不放宽普通供应源的转换策略。
- 画布处理：`gemini_web_image_canvas_tk.go` 只编排，复用 `openai_images_strict_canvas_tk.go`
  的 `resizeImageExactWithinBounds` 与既有渐进缩放/编码；不另实现图片算法。
- catalog、aliases、account floor 与计费：沿用各自现有 SSOT，bundle 由 Go 生成。
- 验收补充：US-056 的 AC-014/015/016；候选入口 owner 仍见 candidate-eligibility SSOT。

## 当前授权与边界

Cookie＋纯 HTTP Worker；Gemini API-key 账号；官方 GenerateContentResponse 外壳。
控制接口沿用 Gemini Web 管理员 API-key。编辑账号导入浏览器 JSON；API Key 框不能粘贴 Cookie。
导入成功仅证明持久化；身份去重 / bootstrap 验证不是完成声明。文件模式已删。


## 兼容协议契约

Messages / Chat / Responses 经 converter → 原生 generateContent。未完成验证 ≠ 已部署。

### 契约

Gemini 是协议，Gemini Web 是受限供应源。传输 profile 负责原生端点和认证，能力 profile
负责可接受的请求；不把 Web 限制施加到普通 Gemini。原始请求先由 `protocolrouter.Plan`
判定，选中账号和 Plan 决定执行。已准入候选的执行平台优先于入站协议的日志提示，
Messages 不再进入绕过 Plan 的旧 Gemini 分支；配额平台保持独立，不能覆盖已准入账号的执行模型命名空间。不能在 converter 中压平历史、删除 system/tools，或者
仅凭账号名、域名和模型别名判断 Web。无能力的候选退出当前请求的竞争，仍在原授权范围内
寻找其他兼容账号；不放宽授权、收费组或供应源凭据检查。

兼容协议统一使用 `generationConfig` 扩展传入 `responseModalities` 和 `imageConfig`，
保留显式 null 和合法比例。三个入口复用现有 Gemini 转换及传输，不新增独立 Web 网关。
回包复用经校验的图片 markdown data URI 文本投影，各协议保留合法 JSON/SSE 外壳；
这一契约保证图片数据可获取，不保证所有客户端都会自动渲染。图片计数来自实际有效输出，
文本响应或错误不能因模型名含 image 而计作一张图片。

缓冲流沿用 Worker 现有语义：完成生成及下载后返回 SSE，不承诺首 token 延迟。
断流不伪造成功，已经输出的内容不自动重放。

**Web 输出上限 = best-effort**（不保证上游执行 token 上限）。Messages 仍须提供合法正整数 `max_tokens`；Chat 的 `max_tokens` /
`max_completion_tokens`、Responses 的 `max_output_tokens` 和原生 Gemini 的
`generationConfig.maxOutputTokens` 具有同样的 Web 限制：网关不保证上游执行该上限。

这项降级仅由 Plan 对显式 Web 供应源处理：验证原始请求，只从不可变 effective request
移除对应输出预算，保留原始正文与 digest，并记录 `gemini_web_best_effort_token_limit`
调整原因。converter 不再猜供应源能力。既有兼容等级排序优先有能力保持原请求的账号；
需要 Web 承接时仍可选到合法的 best-effort Plan。普通 Gemini 原样保留预算；不删除 system、
历史、tools、采样配置或图片输入。缺省预算不制造调整。原生 Worker 的严格 HTTP 契约不变，
该 best-effort 发生在统一网关 Plan，随后发送它本来支持的原生请求。

当前聚焦兼容生图与单轮文本。工具、多轮、多模态输入、会话管理重构、模型目录与定价
变更不能靠静默删字段混进单轮成功路径。

### 生产 canary

复用 `run-probe.sh` / account-model probe；不自动改业务组开关。判定须含 HTTP 成功、
图片解码像素、request ID、usage 账号归属；HTTP 200 / 模型名含 image 不足。
负向须在上游副作用前拒绝或转交合法候选。


## 数据与会话 SSOT

唯一可执行会话为目标网关 的 `accounts.credentials.gemini_web.runtime`：

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

控制路由挂在 `/api/v1/edge/gemini-web`，使用 Gemini Web API-key 和活跃管理员 owner
中间件，不新增 Gemini 专用私网限制或后端服务令牌。

- `GET /accounts/:id/session`：读取该 active、已有 nonempty runtime 的 Gemini API-key 账号 runtime 和 Worker key。不要求 `schedulable`——调度只决定是否进入 fleet，不决定控制面能否读取会话。
- `PUT /accounts/:id/runtime`：以 expected_version 和租约 owner 比较交换 runtime，服务端递增版本；同样只要求 active + 租约，不要求 schedulable。
- `POST /accounts/:id/lease`、`DELETE /accounts/:id/lease`：取得/释放数据库账号租约；active + runtime 即可，与 schedulable 解耦。
- `GET /warm-accounts`：返回到期账号整数 ID 和 `protocol_version=1`，不批量返回 Cookie。默认仅 **schedulable** 账号；例外是 `schedulable=false` 且 `last_refresh=0` 的新导入可进入一次预热（仍排除暂停/冷却/在途租约）。Fleet 常态维护仍由调度门禁保护。

Messages 与 native 两条网关入口都从实际选中账号注入 account ID；
Worker 校验该账号的 API key，不能仅凭 account ID 执行。维护任务使用独立内部调用路径，
不以空 key 假装通过鉴权。

Admin 的“测试账号”复用同一 account ID header owner，绑定本地 runtime 的账号才注入；
prod 中继不携带本地 ID。测试请求按显式 Worker/relay 声明生成单轮文本或
TEXT+IMAGE，不附加 Worker 不支持的 systemInstruction；生图可传入契约允许的
imageConfig.aspectRatio。
普通 Gemini 测试保持原请求形状；用户实际请求的角色、参数不作删减。
与全平台 `AccountTestService` SSOT 对齐：测连不检查、不改写 `schedulable`，也不自动恢复错误或开启调度；导入后保持 `schedulable=false` 时仍应能测通 Worker 会话。

每个账号只有一个 SessionOwner。生成、后台续期、读取最新版本、替换内存和保存共用操作锁。
跨进程租约和 runtime CAS 在同一数据库账号行执行；租约 600 秒，Worker 总操作预算 480 秒，
每次上游请求使用 `(connect, read)` timeout，且 read 不超过剩余预算。默认会话请求
（RotateCookies / `/app` / batchexecute）read 上限 30s；文本 `StreamGenerate` 60s；
出图 `StreamGenerate` 180s；原图下载 90s；connect 上限 15s。到期禁止新请求与写回；
竞争/版本冲突丢弃本地 owner。释放只删除匹配 owner 的租约。故障后不自动重试生成。
Worker 对每次生成记录 `upstream_phase`（`refresh_ms` / `bootstrap_ms` / `generate_ms` /
`generate_timeout_s`），并写入对应 `request` 事件，便于区分冷启动与 GENERATE 挂起。

Worker 每分钟只读维护元数据，只有到期账号才申请租约并执行 RotateCookies → bootstrap，
正常续期间隔十分钟；`schedulable=false` 但 `last_refresh=0` 的新导入也会进入一次预热，
以便测连前完成 bootstrap。暂停、冷却、在途租约和未确认生成不会进入维护列表。
上游成功响应仅在 Cookie jar 变化时写回；`ready_at` / `fields` / `models` 随 state 持久化，
避免 runtime CAS 重建 Account 后重复 bootstrap。runtime 未变化时不重复写入。
蓝绿发版后须保持 Docker DNS 名 `tokenkey` 指向当前 active 应用色（控制面别名代理），
否则 Worker 的 `GEMINI_WEB_CONTROL_URL=http://tokenkey:8080/api/v1` 会解析失败。
生成前持久化 generation_pending，完整成功或明确 quota 拒绝后清除；本次调用收到上游
明确鉴权拒绝且已将会话标记 blocked 时，也清除 pending，但 blocked 继续阻止请求。
本地暂停返回的 403 不清除 pending，连续重试不能绕过暂停。
下载失败或未知生成结果跨重启保持暂停，防止重复消耗额度。
运营在 AdsPower 确认网页可聊后，管理端 **清除错误** 会重置本地 Worker 的
`blocked` / `generation_pending` / `cooldown_until`（无在途 lease 时；保留 Cookie）；
重新导入会话同样重置这些标志。

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
与 SQL 再验证边界。不会自动向远端上游 转发 Cookie。

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
- 初始化成功保持 `schedulable=false`，运营确认后再手动启用调度。启用前可用 Admin
  「测试账号」验证会话（控制面 GET/lease 不依赖 schedulable）；成功摘要是本次已提交版本的回执，不是随后 GET 可能读到的更新版本；账号 DTO
  沿用凭据脱敏。审计整体省略导入请求体。文件解析/读取/API 错误统一显示固定
  文案；409 提示稍后重试，禁止将原始错误中的凭据片段带入 UI。
- 选文件时固定目标 ID；关闭、卸载或换账号使旧操作失效。尚未发送的操作不提交；
  已发送请求仍只作用于原账号，迟到响应不更新新弹窗。同一账号对象刷新不清空摘要。
  导入响应和状态刷新不覆盖编辑中的未保存输入；重新打开、切换账号或账号类型时才回填表单。

运营流程为：复制本地 Worker 账号 → 编辑副本 → 选择凭证 JSON 完成初始化 →
核对导入回执 → 手动启用调度。响应 `session.mode` 为 `initialized` 或 `replaced`；
替换保留原调度状态。一个 Worker 继续按账号 ID 隔离会话，无须为副本新建进程。
升级前已复制且缺少声明的账号不能按名称或分组自动识别；需重新复制真实 Worker 账号。
本轮不迁移旧副本、不修改线上数据。未初始化账号即使误开调度，候选准入也拒绝承接请求。

前端 Playwright 使用真实编辑页面和文件输入、模拟后台响应；数据库并发另用真实
PostgreSQL 验证。两者均不代表线上部署或 Google 会话有效性验证。

普通账号编辑复用已有行锁查询判断会话绑定，无绑定的普通 Gemini 账号不执行额外会话查询。
Worker 启动校验控制协议；只读 `--check` 检查账号绑定、密钥、runtime 和 concurrency=1，
不申请租约或请求 Google，不能证明 Google 会话仍有效。控制请求拒绝重定向以避免转发管理员 key；
该 key 仍要求活跃管理员拥有，本轮不新增 Gemini 专用权限或网络规则。

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

能力边界：本地 Worker 看 `credentials.gemini_web`；远端看
`credentials.gemini_web_relay=true`——不按名称/域名/别名推断。不支持的请求在候选准入剔除，
原授权范围另选兼容账号。输出预算 best-effort 仅 Plan 对显式 Web 供应；普通 Gemini 保留预算。
原生 countTokens 不由 Worker 承接。`probe-gemini-web-relay-declarations.sh` 在含准入逻辑的
镜像部署前核对 `extra.relay_kind=gemini_web` 声明（含关调度账号）；失败挡部署。

图片：original RPC + 受限域名下载、实解码 MIME；不返回预览、不放大、不因下载失败重生；
>1600 万像素拒绝。文本成功且上游无正 token → `tokenestimate`（cl100k）估算，
`billing_tier=gemini-web-estimated`；已有 usageMetadata 优先；生图按次计费。不发明
prompt-cache。五种比例与 `imageConfig: null`=未指定已固化进 Worker。

## Implementation / Owners

| 行为 | 唯一 owner |
|---|---|
| 浏览器导出与 Worker 共用 Cookie 域范围；Go 为测试约束的投影 | ops/gemini-web/session_contract.py；Go/Python 双向契约测试 |
| 管理后台会话导入校验与回执 | backend/internal/handler/admin/account_handler_gemini_web_import.go |
| 本地 Worker 导入资格 | backend/internal/service/gemini_web_request_tk.go 的 CanImportGeminiWebSession；SQL 写入重验 |
| 文件选择、大小限制与弹窗异步生命周期 | frontend/src/components/account/EditAccountModal.vue |
| 会话生命周期及 HTTP 协议执行 | ops/gemini-web/worker.py 的 SessionOwner / Account |
| Worker 控制请求 | backend/internal/handler/gemini_web_session_handler.go |
| 数据库租约与 runtime CAS；导入成功时 error→active（保留 schedulable） | backend/internal/repository/account_gemini_web_tk.go |
| 网关账号引用 | backend/internal/service/gemini_web_tk.go |
| 凭据脱敏与留空保留 | backend/internal/service/account_credentials_redact.go |
| 复制账号重置 | backend/internal/service/admin_account.go |
| 探测结果判定 | ops/stage0/probe_account_model_verdict.py |

DI、路由鉴权和关键调用/测试锚点由 scripts/sentinels/handler-di-wire.json 防回退。
运行命令只放 [Worker README](../../ops/gemini-web/README.md)，验收映射只放
[US-056](../../.testing/user-stories/stories/US-056-gemini-web-http.md)。
