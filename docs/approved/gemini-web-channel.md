---
status: approved
approved_by: user
source_baseline: 75e38806d7f04202f3ee02148f10975b7d26871c
pr_base: 6cfe353d765cf0f2868fcf56d1f22475e79785d3
implementation_approval: user-explicit-database-only-and-review-fixes
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

本轮不部署、不调用真实 Google、不启用商业定价。后台会话导入控件、身份去重与
保存时 bootstrap 验证尚未实现，不属于本轮修复的完成声明。现有 API Key 输入框不能直接粘贴 Cookie。

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
不回显。编辑未提供该字段时保留已有凭据；复制账号清空整个会话绑定及租约，副本保持不可调度。

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

每个账号只有一个 SessionOwner。生成、后台续期、读取最新版本、替换内存和保存共用操作锁。
跨进程租约和 runtime CAS 在同一数据库账号行执行；租约 600 秒，Worker 总操作预算 480 秒，
每次上游请求 timeout 不超过剩余预算。到期禁止新请求与写回；竞争/版本冲突丢弃本地 owner。
释放只删除匹配 owner 的租约。故障后不自动重试生成。

Worker 每分钟只读维护元数据，只有到期账号才申请租约并执行 RotateCookies → bootstrap，
正常续期间隔十分钟；暂停、冷却、在途租约和未确认生成不会进入维护列表。
每次 HTTP 响应保存 Cookie 更新；runtime 未变化时不重复写入。
生成前持久化 generation_pending，只有完整成功或明确 quota 拒绝才清除；
下载失败或未知生成结果跨重启保持暂停，防止重复消耗额度。

普通账号编辑和 credentials 更新在数据库行锁内保留当前会话，防止旧快照覆盖 Worker 更新。
导入必须显式使用当前版本加一；持有在途租约时拒绝导入，不能通过全量 credentials 覆盖绕开并发保护。
未来后台导入控件必须复用此 owner。

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
其他 controls、tools、system、多模态输入和多轮历史在 Google 副作用前拒绝。
streamGenerateContent 返回生成及下载完成后的一条 SSE，不声明首 token 流式延迟。

用户已确认先修复调度能力判断，不扩展或降级上述输入语义。共享候选准入在计费、
选槽及等待后刷新时排除 Worker 不支持的请求，继续在原授权范围选择其他兼容账号。
edge 以 `credentials.gemini_web` 会话对象识别此能力边界；专用 prod 中继显式声明
`credentials.gemini_web_relay=true`，不根据账号名、域名或公开模型别名推断。
现有 Gemini API-key 账号仍走 native 能力 owner，不扩张 protocolrouter 的受管账号范围。
原生单轮文本与生图保留（generateContent / streamGenerateContent）；原生 countTokens
不由 Worker 承接，继续选择其他授权兼容账号。Chat/Responses 转换会补入 Worker 不支持的 maxOutputTokens，
Messages 的 max_tokens 也不能被静默丢弃，因此这些兼容入口不由该受限账号承接。
Anthropic `count_tokens` 继续复用网关本地估算，不受 Worker 生成能力限制。
调度投影与 Worker 共享请求测试样本，防止准入约束漂移。上线需要部署后端并给
prod 中继补入声明；映射、Cookie、并发和调度开关不随代码修复变更。

图片必须经 original RPC 和受限域名下载，实际解码并核对 MIME；不返回预览、不放大、
不因下载失败重新生成。探测同样要求完整解码和正确账号用量归属。
解码前拒绝超过 1600 万像素的图片；图片准入锁覆盖下载、解码及响应缓冲。
不虚构 usageMetadata、modelVersion 或返回内部 thoughts。

## Implementation / Owners

| 行为 | 唯一 owner |
|---|---|
| 浏览器导出与 Worker 共用 Cookie 域范围 | ops/gemini-web/session_contract.py |
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
