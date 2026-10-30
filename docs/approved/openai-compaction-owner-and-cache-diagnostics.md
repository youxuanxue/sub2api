---
title: OpenAI native compaction state owner and cache miss diagnostics
status: archived
archived_reason: "Unapproved proposal retained as historical evidence; implementation remains unapproved."
---

# OpenAI native compaction state owner and cache miss diagnostics

## 背景与范围

这份设计承接 Wei-Shaw/sub2api #8426（native Responses compaction state 可能跨账号）和 #4632（Codex Responses 偶发 `cached_tokens=0`）。它只定义待审批的安全边界和低成本诊断信息，不改变当前线上调度、缓存或计费行为。

现有代码把普通 session 的 soft sticky 和 native compaction state 混在同一选择入口。最小复现已证明：账号 A 产生的 compaction state，在 A 不可调度时，A 的 soft sticky 仍可能失效而选择账号 B；请求上下文没有 `continuationAccountID` 硬归属。账号 B 无法可靠解密 A 产生的密文 state。现有 `invalid_encrypted_content` 恢复还会把 `compaction` / `compaction_summary` 整项剥离后重试，可能把“带历史失败”变成“丢历史成功”。

六次 #4632 样本的完成事件确实报告 `cached_tokens=0`，usage 入账与完成事件一致。五对完整请求保持了历史前缀、工具续轮和 `prompt_cache_key`；缺少的是最终出站身份和上游 cache admission 轨迹。因此本设计不把它们归因于某一处前缀代码。

## #8426：state 硬归属契约

### state 产生时写入 owner

成功响应里只要出现完整 `compaction` 或 `compaction_summary` 输出，就必须登记：包含 native remote compaction v2、普通 Responses 自动产生的 compaction，以及 legacy `/responses/compact` 输出。是否写入由成功输出 state 决定，不以请求有没有 `compaction_trigger` 或 native-v2 marker 决定。记录字段为：

- `tenant_user_id` 与 `api_key_id`：使用现有 Candidate 身份，作为租户隔离边界；
- `state_digest`：对 `encrypted_content` 做 SHA-256，日志和索引只存摘要；
- `producer_account_id`：实际完成该上游请求的账号；
- `producer_namespace`：账号所属供应商/endpoint/ChatGPT account namespace 的脱敏摘要；
- `created_at`、`expires_at`、`last_seen_at` 和协议版本。

`state_digest` 必须同时绑定租户和协议 namespace 的索引键，不能只用密文摘要做全局键。不同租户出现同一摘要时必须互相不可见。同一用户的其他有效 API key 可以复用 state，但仍重新检查当前 key 的授权组与付款资格；这与现有 Responses continuation 契约一致，原 key ID 仅供审计。

建议复用现网 Redis 作为权威 owner 存储，新增独立版本化 key：`openai:compaction-owner:v1:<user-id>:<digest>`；value 为完整结构记录。Lua/CAS 一次性校验并写入 tenant、账号及 namespace，不能复用现有 owner/account 两个可分别写入的键。重复写同 owner 幂等，冲突不覆盖。Redis 不可用时不依赖进程内旧缓存偷偷放行；不增加数据库 schema。

SSE 的 state 发布必须先登记、后返回，不能在 `response.output_item.added` 时把半成品标记可用。建议 collector 按 output_index/item ID 识别 compaction，把该 item 的 added/delta/done 及含 state 的 terminal 暂缓写出；其他普通文字事件仍可流式输出。等成功 `response.completed` 并合成最终完整 state 后，用单次原子存储登记本次全部 state，成功后依序写出暂缓的 compaction 事件及 terminal。item 类型尚未明示的相关 delta 先短暂暂缓，不冒险把未知 state 片段当文字透传。

暂缓只覆盖 compaction 事件，每请求上限建议 8 MiB（密文也计入），超出则在成功终态前以显式错误结束，不截断 state 或继续假成功。此上限是首期内存保护建议，必须根据实际样本调低/调高并观测，不是官方 state 大小限制。传输失败、上游 failed/incomplete、超限或取消均不激活 owner，也不发布完整 state。普通已发文字不等于成功 terminal；写失败时客户端会收到 error event，不能得到一个已成功发布但没 owner 的 state。

非流式 JSON 先登记全部 state，再写成功响应；相同 collector 覆盖 item.done 补全 terminal.output 的现有逻辑。单个响应产生多个 state 时原子登记，不能前几个成功、后一个冲突却返回全部。Redis 写入成功但下游发送失败会保留无害 owner，后续同一 state 写入幂等。

账号 ID 相同但 reauth 换了供应商身份也不等于同 owner。namespace 包含实际供应商、规范化 endpoint、稳定 OAuth subject/workspace；access token 正常续期不改变 namespace。OAuth token 正常刷新且 subject/workspace 未变时继续可用；账号行 reauth 为另一个 subject/workspace 则旧 state 明确失效。API-key 来源以规范化 endpoint+组织/项目+高熵 API key 的摘要为保守 namespace；轮换 API key 后先拒绝旧 state，除非供应商已有可验证的稳定身份能力证明同一 namespace。不得仅按同一账号行放行轮换后的未知凭据，也不得拿邮箱或显示名猜归属。无法确定稳定 namespace 的来源在输出 state 前明确不支持，而不是漏登记后返回成功。

### 读取和选号

带 compaction state 的后续 native 请求先按租户和 state digest 查 owner，再进入现有候选资格、Plan、能力、健康和并发检查：

- owner 存在且账号仍可用：只允许该 `producer_account_id` 执行；不能由 weighted sticky、负载均衡或普通 failover 改选其他账号；
- owner 存在但账号暂不可用：明确返回 `503 compaction_owner_unavailable`，不把 state 发给其他账号；
- owner 记录与请求租户不匹配：按未知 state 处理并明确失败，不能尝试猜测账号；
- owner 不存在、索引损坏或版本不认识：明确返回 state 不可恢复错误，不把带密文的 compaction item 当普通文本继续路由；
- 没有 compaction state 的首次普通请求继续使用现有调度，不强行绑定账号。

这条硬归属适用于携带已登记 native compaction state 的全部重放请求，既包括普通 `/responses` 续聊，也包括再次带 `compaction_trigger` 的压缩轮；不是只保护 trigger 请求。所有以上来源输出的 state 统一登记、统一读取；legacy `/responses/compact` 的既有 capability/model mapping 保持不变。没有 state 的请求保持原有调度。多个 compaction state 必须全部命中同一 owner；与 `previous_response_id` owner 冲突时明确拒绝，不能任选一个。HTTP 和 WebSocket 首帧、后续帧共用解析与校验。

### 生命周期、重试和过期

**待审批寿命建议：30 天闲置过期、从生成时起最多 90 天**。理由是 state 会被客户端存入长会话，现有一小时 soft sticky 不足以承载隔夜/周末恢复；30 天闲置支持常见长会话，90 天绝对上限避免永不过期。该选择是保守产品建议，当前生产 native 压缩计数为零，不能冒称从样本推导出最佳寿命。仅成功使用刷新 idle TTL；不沿用当前默认一小时的 response sticky，否则正常隔夜续聊会无故失效。保留 owner 不占并发槽或预留账号容量。该寿命是 TokenKey 可恢复窗口，不是供应商有效期承诺；较短窗口节省记录、较长窗口支持长期会话。上线前必须明确选定，不能藏在实现默认值里。

Redis 写入失败时不得宣称绑定成功：终态前返回 `503 compaction_state_store_unavailable`（流已开始用等价 error event），不发送成功 terminal；已产生的真实上游 usage 仍沿现有计量契约记录，不可通过失败漏账。用户可见错误不输出 tenant、账号编号或密文。

同一账号上的网络重试可以复用 owner；不允许跨账号重试。若上游明确返回 state 无效，记录 digest 的失效状态并返回错误；不能删除 compaction item 后再次发送一个语义不同的请求。

过期、未知或跨租户 state 统一对外返回 `400 compaction_state_unavailable`，指向 `input`，要求客户端从仍保留的明文历史建立新会话；网关不假设自己能恢复已被压缩替代的历史。owner 冲突返回 `400 compaction_state_conflict`；已知 owner 暂不可用返回 `503 compaction_owner_unavailable`。并发已满沿原 owner 的既有 wait/429 预算，不能转 B。内部指标区分 unknown/expired/conflict/store_error，不对客户泄露他人 state 是否存在。

启用前旧 state 没有可信 owner，不能用 sticky 回填。建议先发布仅记录成功输出 owner 的阶段，再明确切换 enforcement；此期间老 state 仍可能重放但不能声称已安全修复。正式 enforcement 时未知旧 state 明确失败，此兼容成本需要审批。回滚 enforcement 会恢复旧跨账号风险，必须记录回滚窗口。清理只删除索引和摘要，不触碰账单记录。

### 防止静默丢失压缩上下文

`invalid_encrypted_content` 恢复策略继续适用于 reasoning 密文，但当请求包含 `compaction` 或 `compaction_summary` 时禁止整项剥离和重试。应在上游调用前或收到拒绝后返回参数/状态错误，并保留原请求证据摘要。测试必须证明不会出现“删除压缩 state 后上游 200”的路径。混合 reasoning+compaction 的 invalid_encrypted_content 不具备定位到哪一块的证据时，也不能靠删 reasoning 后跨账号或改上下文重试。

## 审批项 A：compaction 语义与兼容成本

请决定是否采用：全部 compaction 输出统一登记；同用户跨 key 但重新授权；未知/过期旧 state 明确 400、原 owner 不可用明确 503；30 天 idle/90 天绝对寿命；8 MiB 暂缓上限；停止剥掉 compaction 后重试。建议整体采用，先部署仅登记再安排 enforcement 切换。用户批准前不实现这套新失败策略或安全边界。

## #4632：低成本缓存诊断

复用现有请求日志关联与 usage 日志，不新增正文存储、队列或 raw archive。建议配置默认关闭、启用时按租户/稳定会话确定性抽样 1%，让选中会话连续各轮都记录；不能每轮独立抽样造成无法比较。最终上游 body、headers、账号、proxy 和服务档位全部确定后、`httpUpstream.Do` 前记录每次真实 attempt，完成解析点补记结果。失败 attempt 也要记录元数据，不能只看最终成功账号。首期只覆盖六条样本相同的 OAuth HTTP Responses 路径，其他路径显式标为 coverage=false，不宣称已覆盖 WS/Chat。

使用进程启动生成的随机 HMAC key，内存保存不落盘；每次启动生成可公开的 epoch ID，跨 epoch 不比较。tenant ID 纳入 HMAC domain；敏感值不得用裸 SHA256。缺稳定会话时跳过并计数，不凭正文猜新跨会话身份。字段包括：

- 最终 instructions/tools 原始字节摘要；input 逐项累积链式 HMAC，最多保留 1024 个边界，超限标记 truncated（不能宣称完整前缀相等）；
- `prompt_cache_key` 是否存在及其脱敏摘要；
- 最终模型、服务档位、账号 credential namespace 和 proxy 摘要；最终 session/conversation/thread/installation 身份头和影响缓存身份的白名单元数据只记录租户隔离 HMAC，不记录原值；
- 每次 attempt 的关联 ID、账号是否变化、重试原因；
- 上游完成事件中 cache 字段的状态（missing、zero、positive）及数值；
- 上游 request ID（已有字段时）。

不得把 prompt、工具参数、原始 key、Authorization、cookie 或完整出站 body 写入普通日志。随机 turn ID 不得混入稳定前缀摘要。接入既有日志保留/清理 owner，新增诊断 component 的记录保留不超过 24 小时；必须验证实际删除时效，不能只用查询时间过滤冒充 retention。如果既有 sink 不能逐 component 保留，则在确定可执行的现有清理方案前不开启落盘，采样额外设每分钟 100 条事件上限。每条只保留有限摘要/计数，不保留正文。容量截断和采样丢弃有计数。零缓存 usage 本身可全量计数，但缺出站采样的零缓存事件标记 evidence_incomplete，不能据此推断根因。该观测配置与 retention 开启需另行上线，不属于本次只读核查。

验收时用同一账号、同一会话和工具续轮构造一对请求：应能判断稳定前缀是否变化、账号/namespace 是否变化、以及上游返回的是 missing 还是明确 zero。若摘要和账号均一致但再次出现 zero，则把脱敏摘要和上游 request ID 交供应商核查 cache admission/shard；不能继续猜测或自动改变账号粘性、failover 或 cache key。

## 审批项 B：缓存观测开关

与 compaction 业务改动独立决定。建议只启用 OAuth HTTP Responses 的 1% 会话采样、24 小时保留、每分钟 100 条事件预算、最多 1024 input 边界；起步比例低是为了先验证 metadata 覆盖和成本，不是承诺 1% 必然捕获下一次低频 miss。需要扩大采样时根据实际丢弃率和日志量决定，不能自动升级正文采集。会话抽样能保留相邻轮次；临时 HMAC key 让同进程可比、跨重启不可比，避免为一次诊断引入长期秘密管理。

## 验证与回退

设计获批后，先增加失败测试和最小状态存取测试，再实现 owner 写入、租户隔离读取、过期/未知/冲突错误、同账号重试和 compaction 禁止剥离。集成测试必须覆盖 A 产 state、A 不可调度时拒绝 B、租户交叉读取失败，以及 reasoning-only 恢复仍保持旧行为。线上验收先只读检查 owner 命中和拒绝计数，不发送真实压缩会话。

实现涉及的边界由既有 owner 接入：`candidate_request_tk.go` 解析硬 owner，`candidate_selection_tk.go` 复用现有唯一账号过滤；`openai_gateway_response_handling.go`/WS 成功终态共用输出登记 helper；`openai_encrypted_content_lineage.go` 和所有 HTTP/WS 重试路径共用不可剥 compaction 判定。不能另建与 Plan 竞争的选号器。

可运行最小复现源码和日志分别保存在 `/tmp/openai_issue8426_diagnostic_tmp_test.go`、`/tmp/openai8426-diagnostic1030.log`，基于 `origin/main@130cb33c8`。复现覆盖普通重放和再次压缩：A 不可调度时当前选 B，手工设置真实硬 owner=A 的对照会拒绝 B。该测试确认缺陷机制，不是修复验收。

所有新增状态均按版本管理；回退时保留旧记录自然过期，不删除账单和用户数据。实现前需要审批未知/旧 state 的失败策略、30/90 天寿命与上线切换，以及诊断 1% 采样/24h retention。线上模型调用和付费压缩不包含在本次审批。
