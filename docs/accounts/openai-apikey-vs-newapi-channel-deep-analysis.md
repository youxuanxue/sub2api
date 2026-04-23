---
title: "TokenKey OpenAI(API Key) 账号 vs New-API 渠道 — 深度分析与第五平台 newapi 必要性判定"
status: draft
authors: [agent]
created: 2026-04-23
related_designs:
  - docs/approved/newapi-as-fifth-platform.md
  - docs/approved/admin-ui-newapi-platform-end-to-end.md
  - docs/approved/newapi-followup-bugs-and-forwarding-fields.md
  - docs/approved/sticky-routing.md
related_flows:
  - docs/flows/openai-gateway-flow.md
  - docs/flows/newapi-messages-gateway-flow.md
audience: ["架构师", "产品", "运维"]
---

# OpenAI(API Key) 账号 vs New-API 渠道 — 深度分析与第五平台 `newapi` 必要性判定

> **结论先行**：两种添加方式**不是替代关系**，而是**两层不同语义**。
>
> - TokenKey 现有的 `OpenAI + API Key` 账号 = "把一个 OpenAI 协议密钥/网址挂到 OpenAI 调度池里，由 TokenKey 的订阅/计费/调度链路统一治理"。
> - New-API 添加渠道 = "把任意一个上百种异构上游（OpenAI、Anthropic、Gemini、Baidu、Volcengine、Midjourney、Suno…）通过 new-api 的 adaptor 协议归一为 OpenAI/Claude/Gemini compat 暴露给客户端，按 token×modelRatio×groupRatio 计费"。
> - **第五平台 `newapi`（已 ship 于 v1.4.x）** 是一种**桥接策略**：在 TokenKey 现有"账号 / 分组 / API Key / 订阅"治理框架内，复用 new-api 的 channel adaptor 来一次性接入剩下的几十种上游协议——它**回答的是"我想要 TokenKey 的订阅/审计/账号粘性，但又想接入 Moonshot / DeepSeek / Volcengine / Vertex 这些非四大平台"**。
>
> 因此问题的本质不是"二选一"，而是**"在什么场景下应该用 OpenAI(API Key) 直挂、什么时候必须走第五平台 `newapi`"**。本文回答这个问题，并复盘已 ship 的 `newapi` 平台是否在 TokenKey 的产品定位下保有持续的必要性。

---

## 0. 背景与边界

### 0.1 为什么会有这个问题

截图 1 是 TokenKey 后台 `添加账号 → OpenAI → API Key` 的真实表单：备注、Base URL、API Key、模型限制（白名单/映射）、池化模式、自定义错误码、配额控制（启用/临时不调度/调度倍率）、代理、并发数/负载因子/优免级/账号计费倍率、过期时间、自动续签、WS mode、过期自动暂停、分组（多选）。

截图 2 是 New-API 后台 `创建新的渠道` 的真实表单：类型（下拉，几十种）、名称、密钥、API 地址（可空）、模型（多选 + "填入相关模型"按钮）、自定义模型名称、分组（多选）、模型重定向（可视化/手动 JSON 编辑）。

肉眼看上去最显眼的两点差异：

| 维度 | TokenKey OpenAI/API Key | New-API 创建渠道 |
|---|---|---|
| 类型选择 | 1 个固定平台（OpenAI） | 数十种类型（每种背后是一个 channel adaptor） |
| 配额/调度配置 | 明确暴露（启用/暂停/倍率/并发/优免级/池化） | 仅类型 + 密钥 + 分组 + 模型 + 重定向 |
| 鉴权流变 | OpenAI OAuth 与 API Key 分两个 segment | 不区分；统一一个 "密钥" 字段 |
| 计费视角 | 隐含在分组/订阅/usage_log，由 TokenKey 自己治理 | 显式在系统层（model_ratio × group_ratio × user_ratio） |
| 模型治理 | 白名单 / 模型映射；模型来源由网关侧 PricingResolver 统一 | adaptor 自带 default model list；运营侧可以"填入相关模型" |

但这只是表象差异。下面**按"中转链路"和"计费链路"两条主线逐层下沉**，才能看清两种方式的真实差异。

### 0.2 术语对齐

- **TokenKey 账号**（`backend/internal/service/account.go`）：`(platform, type)` 二元组下的可调度单元。`platform ∈ {anthropic, openai, gemini, antigravity, newapi}`；`type ∈ {oauth, apikey, setup_token, upstream}`，并不是所有组合都合法。
- **TokenKey 分组**（Group）：调度域 + 计费配置的容器。一个分组只属于一个 platform；一个 API Key 绑定到一个分组；调度器只在分组内挑账号。
- **TokenKey API Key**：下游客户端拿到的钥匙；唯一通过 `apiKeyAuth` 中间件解锁运行时上下文。
- **New-API 渠道**（Channel）：在 new-api 数据模型里，一个 channel = `(channel_type, base_url, api_key, model_list, model_mapping, group_list)`，运行时按 `model + group` 选 channel；**channel 自身不暴露给下游**——下游拿到的是 new-api 的 token，由 new-api 选择 channel。
- **New-API token**：等价于 TokenKey 的 API Key，但 new-api 的 token 默认按 token×ratio 直接扣 quota，而不是按"订阅/余额"区分。

把术语对齐之后，能看到两边数据模型不是 1:1：
- TokenKey: `User → APIKey → Group → Account`
- New-API: `User → Token → Group → Channel`

形似实异。下面拆解差异。

---

## 1. 中转逻辑差异

### 1.1 TokenKey 的中转链（OpenAI + API Key 路径）

权威实现：`backend/internal/service/openai_gateway_service.go`（约 5300 行）+ `openai_gateway_handler.go` + 路由 `routes/gateway_tk_openai_compat_handlers.go`。

```
HTTP /v1/chat/completions | /v1/messages | /v1/responses | /v1/embeddings | /v1/images/generations
  └─ Middleware: RequestBodyLimit → ClientRequestID → InboundEndpointMiddleware
                 → apiKeyAuthWithSubscription (注入 apiKey + group + user + subscription 到 ctx)
                 → requireGroup{Anthropic|OpenAI|...}
                 → tkOpenAICompat*POST (路由分发)
  └─ Handler: OpenAIGatewayHandler.Messages|ChatCompletions|Responses|...
       1. ResolveMessagesDispatchModel(requestedModel)        // 按分组配置把 claude-* 映射到 gpt-*
       2. ResolveChannelMappingAndRestrict()                  // 渠道层模型重写 + 限制
       3. BillingCacheService.CheckBillingEligibility()       // 余额/订阅可用性检查（pre-check，不预扣）
       4. GenerateSessionHash() → SelectAccountWithScheduler()
              ├─ getStickySessionAccountID (Redis sticky)
              ├─ ListSchedulableAccounts(groupID, group.platform)   // 按 group.platform 分桶
              └─ filterAndRank: IsSchedulable + IsModelSupported + RequirePrivacy + 负载/排队/错误率/TTFT
       5. ForwardAsAnthropicDispatched/ForwardAsResponsesDispatched/...
              if ShouldDispatchToNewAPIBridge(account, endpoint):
                  → bridge.Dispatch* → new-api adaptor (channel_type 决定走哪个上游)
              else:
                  → Forward* (直连 chatgpt.com 或 api.openai.com，按 account.Type 区分)
       6. RecordUsage()   // 异步扣费 + 写 usage_log
```

对**OpenAI + API Key** 账号（截图 1 的场景），关键事实：

1. **`base_url` 会被严格校验**：`validateUpstreamBaseURL` 拒绝非法 host、`..`、文件协议；写入数据库前调用，运行时再次校验。
2. **`Forward()` 的上游 URL 推导**：见 `openai_gateway_service.go::buildUpstreamRequestOpenAIPassthrough` —
   - `AccountTypeOAuth` → `https://chatgpt.com/backend-api/codex/responses`
   - `AccountTypeAPIKey` → `account.GetOpenAIBaseURL()` + `/responses`（默认 `https://api.openai.com/v1/responses`，可被 base_url 覆盖）。
3. **鉴权头**：API Key 路径直接 `Authorization: Bearer {api_key}`；OAuth 路径走 token provider 拿到的 access_token + 必要的 ChatGPT internal headers（`chatgpt-account-id`、`session_id`、`accept: text/event-stream` 等）。
4. **协议入口形态**：API Key 账号目前**只能进 OpenAI 兼容入口**——`/v1/chat/completions`、`/v1/messages`（通过 dispatch model 反向落到 chat completions）、`/v1/responses`、`/v1/embeddings`、`/v1/images/generations`。**没有** Anthropic 原生 `/v1/messages` 走法（`requireGroupAnthropic` 仅放行 platform=anthropic 的分组），也没有 Gemini 原生路径。
5. **协议归一**：所有 `/v1/messages`（Claude 协议）请求都先落到分组的 `messages_dispatch_model_config`，再翻译成 OpenAI Responses/Chat Completions。这意味着 `OpenAI + API Key` 账号本质上就是个"兼容 OpenAI Responses/Chat Completions schema 的上游"。
6. **失败切换**：`shouldFailoverUpstreamError` 命中 401/402/403/429/529 + 5xx 时调度器降级到下一个候选账号；同时 `rateLimitService.HandleUpstreamError` 抓取响应头/体，更新账号的限流/暂停状态。
7. **WS mode**：`openai_apikey_responses_websockets_v2_mode` 控制是否走 OpenAI 内部的 Responses WebSocket v2 协议；只对账号本身生效，state 留在进程内（不能跨实例 LB）。

### 1.2 New-API 的中转链

权威实现：`/new-api/relay/` + `/new-api/controller/relay.go` + 上百个 `relay/channel/<provider>/adaptor.go`。

```
HTTP /v1/chat/completions | /v1/messages | /v1/responses | /v1/audio/* | /v1/images/* | /v1/rerank | /v1/realtime | /mj/* | /suno/* | ...
  └─ Middleware: tokenAuth → distribute()
                 (按 model + group 调用 service.SelectChannel → channel_select.go)
  └─ Controller: relay.Relay() 按 RelayMode 分派到具体 RelayHelper
  └─ Helper: relay/<mode>_helper.go
       1. PreConsumeBilling(预估 quota，按 model_ratio*group_ratio*PromptTokens 估算)
       2. relay.GetAdaptor(apiType)  // OpenAI/Anthropic/Baidu/Gemini/Tencent/Aws/Vertex/...
       3. adaptor.Init(relayInfo) → adaptor.GetRequestURL/Headers
       4. adaptor.ConvertRequest(originalReq) → 上游协议体
       5. adaptor.DoRequest()       // 发出请求
       6. adaptor.DoResponse()      // 解析响应（含 usage 抽取）
       7. SettleBilling(实际 quota)  // 后结算 + 写 logs.consume_log
```

关键事实：

1. **adaptor 是协议转换器**：每个 `channel_type` 对应一个 `apiType`，`apiType` 选定 adaptor。adaptor 知道如何把 OpenAI 协议请求翻译成上游真实协议（百度文心的 access_token 流、Vertex 的 GCP 鉴权、Bedrock 的 SigV4、Azure 的 deployment URL……），并把上游响应翻译回 OpenAI/Claude/Gemini compat schema。
2. **`channel_type → api_type` 是核心枚举**：见 `/new-api/common/api_type.go::ChannelType2APIType`。共注册了 ~37 个 adaptor（OpenAI / Anthropic / PaLM / Baidu / Zhipu / Ali / Xunfei / Tencent / Gemini / Zhipu_v4 / Ollama / Perplexity / AWS Bedrock / Cohere / Dify / Jina / Cloudflare / SiliconFlow / Vertex / Mistral / DeepSeek / MokaAI / VolcEngine / BaiduV2 / OpenRouter / Xinference / Xai / Coze / Jimeng / Moonshot / Submodel / MiniMax / Replicate / Codex / …）。
3. **协议入口范畴**：`Path2RelayMode`（`/new-api/relay/constant/relay_mode.go`）覆盖 OpenAI Chat/Completions、Embeddings、Moderations、Images（gen/edits）、Edits、Audio Speech/Transcription/Translation、Rerank、Responses、Responses Compact、Realtime、Gemini `/v1/models`、Midjourney 全套（imagine/blend/describe/change/swap/video/edits/notify/fetch）、Suno 全套（fetch/submit）、Video（fetch/submit）。**这是 OpenAI + API Key 单点上游远不能覆盖的全集**。
4. **distribute / select_channel**：调度只在"channel.model_list 命中 request.model"的子集中按 `priority + weight + balance` 选；不需要"分组只挂同种 channel_type"。也就是说 new-api 的同一个 group 里可以挂混合 channel（GPT-4 走 OpenAI 官方、Claude 走 Anthropic、Moonshot 走 Moonshot、Vertex 走 Vertex），按 model 匹配自动路由。
5. **失败切换**：channel 上配置 `auto_disable + status_code_mapping`；upstream 5xx/429 命中 `shouldDisableChannel` 时自动暂停。

### 1.3 中转逻辑核心差异（事实对比）

| 中转维度 | TokenKey OpenAI(API Key) | New-API 渠道 | 差异本质 |
|---|---|---|---|
| 上游协议覆盖 | 仅 OpenAI 协议族（Chat Completions / Responses / Embeddings / Images） | 30+ 协议族（含 Anthropic / Gemini / Baidu / Vertex / Bedrock / Volcengine / Moonshot / DeepSeek / Cohere / Mistral / Midjourney / Suno / Realtime…） | TokenKey 是单协议网关；new-api 是"协议归一层" |
| 请求体/头转换 | 不需要做协议翻译；只在 `/v1/messages` ↔ Responses 之间做有限映射（messages_dispatch） | 每个 channel_type 一个 adaptor，全套 ConvertRequest/ConvertResponse | TokenKey 假设上游与下游 schema 同源；new-api 假设上游异构 |
| 调度域 | 一个分组只挂同 platform 同 type 的账号；按 group.platform 分桶 | 一个分组可混挂任意 channel_type，按 model 命中过滤 | TokenKey 强类型分组；new-api 弱类型混合 |
| 鉴权头注入 | API Key → `Bearer api_key`；OAuth → `Bearer access_token + ChatGPT headers` | adaptor 自定义（百度 access_token 刷新、Vertex GCP 鉴权、Bedrock SigV4、Azure deployment 等） | new-api 把"奇怪鉴权"封装在 adaptor；TokenKey 没有这层 |
| sticky / cache | sessionHash → account_id（Redis），prompt_cache_key 自动派生（gpt-5.4 / 5.3-codex 白名单） | channel 亲和性（GLM 风格 X-Session-Id），由 adaptor + setting 决定 | TokenKey 把 sticky 抬到网关层；new-api 把 sticky 下沉到 adaptor |
| WS / 长连接 | OpenAI Responses WSv2、本地状态 store（不跨实例） | Realtime + Suno 长任务（webhook 拉取） | TokenKey 主打 OpenAI WSv2；new-api 主打异步任务模型 |
| 失败切换 | shouldFailoverUpstreamError + RateLimitService（HandleUpstreamError）+ MaxAccountSwitches | auto_disable + status_code_mapping + 重试 | 行为相近，TokenKey 多了"调度器层"切账号；new-api 多了"channel auto_disable" |
| 错误透传 | `ErrorPassthroughService` + 自定义错误码（截图 1 字段） | adaptor 自带错误体翻译 | TokenKey 暴露了 admin 配置面；new-api 黑箱化 |

### 1.4 孰优孰劣（按场景看）

不存在一个绝对优劣的判断；要分场景：

| 场景 | TokenKey OpenAI(API Key) 直挂 | New-API 渠道（独立部署） | 第五平台 `newapi`（桥接） |
|---|---|---|---|
| 把一个 OpenAI 第三方 KEY 接到订阅化 SaaS（Claude Code 用户群） | **最优**：直接挂、订阅扣费、账号粘性、模型映射统一 | 不优：需要再造一层 token / 订阅 | 不必要：上游就是 OpenAI compat |
| 把 OpenAI OAuth ChatGPT Team 账号转 API | **唯一可行**：本就是 TokenKey 的设计目标（Wei-Shaw/sub2api 起源） | 不可行：new-api 不接 ChatGPT OAuth | 不可行 |
| 接入 DeepSeek / Moonshot / Volcengine / Baidu / Vertex / Bedrock | 不支持（base_url 写得对也只到协议层；DeepSeek/Moonshot 因兼容 OpenAI schema 勉强能跑，Vertex/Bedrock/Baidu 完全跑不通） | **最优**：adaptor 完整覆盖 | **TokenKey 唯一可行**：复用 new-api adaptor，仍走 TokenKey 订阅链路 |
| 接入 Midjourney / Suno / Realtime | 不支持 | **唯一可行**：协议归一 | 部分可行（bridge 已支持 chat_completions/responses/embeddings/images，未支持 mj/suno/realtime） |
| 给一个企业内部混合 LLM（GPT + 自家 LLM + 第三方 LLM）做统一计费、模型 ratio、限速 | 不优：每种上游都要重新造调度/计费 | **最优**：天然就是 new-api 的产品定位 | 不优：TokenKey 的 group 强类型设计不适合"混挂"场景 |
| Claude Code / cc switch 用户接入 OpenAI/Anthropic 订阅 + 偶尔的 newapi 兜底 | 主路径必须是直挂；兜底用第五平台 newapi | 不优：会丢掉 OpenAI OAuth ChatGPT Team 的特殊处理 | **最优**：保留主路径，第五平台兜底 |

**结论**：TokenKey 的 OpenAI(API Key) 直挂 = 把 TokenKey 当 SaaS 网关用；new-api = 把它当多 LLM 聚合代理用；第五平台 `newapi` = 在前者形态里偷偷复用后者的 adaptor 库。

---

## 2. 计费逻辑差异

### 2.1 TokenKey 计费链（参考 `billing_service.go::CalculateCostUnified` + `openai_gateway_service.go::RecordUsage`）

数据来源：

- 模型定价：**项目内置 + 可热更新的 model-pricing 表**（`backend/resources/model-pricing/` + `model_pricing_resolver.go`），按模型 + groupID 解析；支持 token 区间定价、长上下文倍率、cache_write_5m/1h、image_output、service_tier、按次计费。
- 账号倍率：`account.BillingRateMultiplier`。
- 分组倍率：`apiKey.Group.RateMultiplier`，可通过 `userGroupRateResolver` 用 user×group 重算。
- 全局倍率：`cfg.Default.RateMultiplier`。

计算步骤：

1. 后置结算（pure post-consume），不预扣 quota；进入前先 `BillingCacheService.CheckBillingEligibility` 做"余额/订阅是否够付一次最小请求"的可用性判断（缓存命中走 redis）。
2. 调度账号成功 + 上游响应回来 + 拿到 usage（`InputTokens / OutputTokens / CacheCreationInputTokens / CacheReadInputTokens / ImageOutputTokens`）。
3. 调用 `BillingService.CalculateCostUnified(model, groupID, tokens, multiplier, serviceTier, resolver)`：
   - 若 `BillingMode = token`：按 token 区间 + cache_write 5m/1h 拆分 + 长上下文倍率，得到 `inputCost / outputCost / cacheCreationCost / cacheReadCost / imageOutputCost / totalCost`，再乘 `rateMultiplier`（含 service_tier 倍率）。
   - 若 `BillingMode = per_request`：按 size_tier 选单价 × request_count × rateMultiplier。
   - 若 `BillingMode = image`：同上。
4. 根据分组类型选计费源：
   - `BillingTypeBalance`：扣 user.balance（`postUsageBillingParams::applyUsageBilling` 走 wallet 路径）。
   - `BillingTypeSubscription`：扣 `user_subscription.remaining_quota`，超额按规则降级。
5. 写 `usage_logs`（含 `RequestedModel / Model / UpstreamModel / ModelMappingChain / ChannelID / BillingMode / ServiceTier / ReasoningEffort / Stream / OpenAIWSMode / DurationMs / FirstTokenMs / RateMultiplier / AccountRateMultiplier`）。
6. **完全后置**：失败也只在已扣的部分扣，没有"预扣 + 退款"链；这降低了一致性复杂度，代价是极端突发可能短暂超额，由 `BillingCacheService` 做 best-effort 拦截。

### 2.2 New-API 计费链（参考 `service/billing.go` + `service/billing_session.go` + `service/text_quota.go`）

数据来源：

- 模型定价：`setting/ratio_setting/model_ratio.go::defaultModelRatio`（约 700 行硬编码 + DB override），单位 `1 ratio = $0.002 / 1k tokens`，全局表。
- 用户分组倍率：`group_ratio.go::defaultGroupRatio`。
- Cache 比例：`cache_ratio.go`。
- 完成倍率：`CompletionRatio`（输出 token 与输入 token 单价之比）。

计算步骤：

1. **前置预扣**：`PreConsumeBilling(c, preConsumedQuota, relayInfo)`——按 PromptTokens × modelRatio × groupRatio 预估并立即扣 user.quota。
2. adaptor 发请求 → 拿到 usage。
3. **后置结算**：`SettleBilling(actualQuota)`——
   ```
   delta = actualQuota - preConsumedQuota
   if delta != 0: 补扣或返还
   ```
4. 写 `logs.consume_log`（含 model_name / group / channel_id / use_time / prompt/completion/cache_tokens / quota / model_ratio / group_ratio / completion_ratio / model_price / cache_ratio / web_search_call_count / claude_web_search_price / image_generation_call_price 等）。

### 2.3 计费逻辑差异（事实对比）

| 计费维度 | TokenKey | New-API | 差异本质 |
|---|---|---|---|
| 计费时序 | 纯后置（best-effort cache 做 pre-check） | 前置预扣 + 后置结算补扣/返还 | TokenKey 简单稳定但极端场景可能短暂超额；new-api 严格防超额但代码复杂度高 |
| 单价表 | model-pricing 资源 + 区间/长上下文/cache_5m_1h/服务等级；可按 group override | model_ratio 硬编码 + DB 可覆盖；group_ratio + cache_ratio + completion_ratio | TokenKey 表更精细（区间 + 5m/1h cache 拆分 + service_tier）；new-api 表更广（700 行覆盖几百模型，含 web_search/file_search/audio_input 等扩展） |
| 计费源 | wallet 余额 / 订阅 quota（user_subscription） | 仅 wallet quota（user.quota） | TokenKey 双计费源是 SaaS 订阅化的核心；new-api 是计量计费 |
| 倍率维度 | 全局 × 分组 × 账号 × service_tier | 全局 × 分组 × 模型 | TokenKey 多了 account-level + service_tier；new-api 多了 web_search/file_search 等"动作计费" |
| 失败处理 | 上游失败不计费（Forward 失败前 RecordUsage 不会被调用） | 预扣后失败由 SettleBilling 返还 | 两边等价 |
| usage_log 维度 | `RequestedModel / Model / UpstreamModel / ModelMappingChain / ChannelID / BillingMode` | `model_name / channel_id / use_time / prompt/completion/cache / model_ratio / group_ratio / model_price` | TokenKey 多了"模型映射链"审计；new-api 多了"动作维度"扩展 |
| 长上下文计费 | `LongContextInputMultiplier / LongContextOutputMultiplier`，threshold 触发 | 通过 `defaultModelRatio` 中 `*-200k` 等独立模型条目隐式表达 | TokenKey 单字段触发；new-api 走"独立模型"枚举 |
| 缓存计费 | `cache_creation_5m / cache_creation_1h / cache_read` 三档独立单价 | `cache_ratio` 倍率统一处理；ImageRatio 单独 | TokenKey 接近 Anthropic ephemeral cache 真实模型；new-api 套了一层倍率 |

### 2.4 计费孰优孰劣

- **稳定性**：TokenKey 的"纯后置"模型简化了一致性，但牺牲了"绝对不超额"；new-api 的"预扣 + 结算"严格但需要处理失败返还、并发更新 quota 的竞态。对 TokenKey 的目标用户群（Claude Code / cc switch 一类的低 QPS 长任务）来说，简化模型更合适。
- **精细度**：TokenKey 在 cache 5m/1h、service_tier、长上下文、按次/按图独立计费上比 new-api 颗粒度更细；new-api 在"非聊天动作"（web_search、file_search、image_generation_call）上有专门字段，TokenKey 暂未覆盖。
- **可治理度**：TokenKey 的订阅 + 余额双源 + 账号倍率模型，对面向多用户、多团队、多产品包的 SaaS 形态更合适；new-api 的纯 quota 模型对企业内部聚合代理更合适。

---

## 3. 协议范畴对比

### 3.1 New-API 已支持的渠道类型清单（来源：`/new-api/constant/channel.go`，54 项 + Dummy）

`ChannelTypeNames` 共 54 个有效条目，按归类：

- **OpenAI 协议族 / 兼容**：OpenAI(1) / Azure(3) / OpenAIMax(6) / OhMyGPT(7) / Custom(8) / AILS(9) / AIProxy(10) / API2GPT(12) / AIGC2D(13) / OpenRouter(20) / AIProxyLibrary(21) / FastGPT(22) / DeepSeek(43) / MokaAI(44) / SiliconFlow(40) / Mistral(42) / LingYiWanWu(31) / Moonshot(25) / Xinference(47) / Xai(48) / Submodel(53) / Codex(57)
- **国产大模型原生协议**：Baidu(15) / BaiduV2(46) / Zhipu(16) / Zhipu_v4(26) / Ali(17) / Tencent(23) / Xunfei(18) / 360(19) / VolcEngine(45) / Coze(49) / MiniMax(35)
- **海外大模型原生协议**：Anthropic(14) / Gemini(24) / Aws Bedrock(33) / Cohere(34) / Perplexity(27) / VertexAi(41) / Replicate(56)
- **图像/视频/音频**：Midjourney(2) / MidjourneyPlus(5) / Kling(50) / Jimeng(51) / Vidu(52) / DoubaoVideo(54) / Sora(55) / SunoAPI(36) / Cloudflare → Workers AI(via custom)
- **应用平台**：Dify(37) / Jina(38)
- **本地推理**：Ollama(4)
- **PaLM(11)**: 已废弃留枚举

每条 channel_type 在 adaptor 层是一个独立的 protocol implementation，包含 `GetRequestURL / GetRequestHeaders / ConvertRequest / DoRequest / DoResponse / GetModelList`。

### 3.2 New-API 支持的 RelayMode（API 入口端）

来源：`/new-api/relay/constant/relay_mode.go`，覆盖：

| RelayMode | 路径 | 含义 |
|---|---|---|
| ChatCompletions | `/v1/chat/completions` | OpenAI 主路径 |
| Completions | `/v1/completions` | 旧 text completions |
| Embeddings | `/v1/embeddings` | 向量 |
| Moderations | `/v1/moderations` | 内容审核 |
| ImagesGenerations | `/v1/images/generations` | 文生图 |
| ImagesEdits | `/v1/images/edits` | 图编辑 |
| Edits | `/v1/edits` | 旧 edits |
| Responses | `/v1/responses` | OpenAI Responses API |
| ResponsesCompact | `/v1/responses/compact` | OpenAI Responses 压缩协议 |
| Realtime | `/v1/realtime` | OpenAI Realtime（WS） |
| Gemini | `/v1beta/models` `/v1/models` | Gemini 协议入口 |
| AudioSpeech | `/v1/audio/speech` | TTS |
| AudioTranscription | `/v1/audio/transcriptions` | Whisper STT |
| AudioTranslation | `/v1/audio/translations` | Whisper 翻译 |
| Rerank | `/v1/rerank` | 重排 |
| Midjourney×13 | `/mj/submit/*` `/mj/notify` ... | MJ 完整任务模型 |
| Suno×3 | `/suno/submit/*` `/suno/fetch` | Suno 异步任务 |
| Video×2 | `/video/submit` `/video/fetch` | 视频异步任务 |

### 3.3 TokenKey 现状（main 分支 v1.4.x）支持的 RelayMode

来源：`backend/internal/server/routes/gateway.go` + `routes/gateway_tk_*.go`：

- **Anthropic 协议族**（`platform=anthropic` 分组）：`/v1/messages` 原生（直连 Anthropic API 或 Anthropic OAuth）
- **OpenAI 协议族**（`platform=openai` 或 `platform=newapi` 分组）：`/v1/chat/completions`、`/v1/messages`（messages_dispatch 翻译为 Responses/ChatCompletions）、`/v1/responses`、`GET /v1/responses`（WSv2）、`/v1/embeddings`、`/v1/images/generations`
- **Gemini 协议族**（`platform=gemini` 分组）：`/v1beta/models/*:generateContent` / `:streamGenerateContent`、`/v1/models/*` 类似
- **Antigravity 协议族**（`platform=antigravity` 分组）：Cloud Code Antigravity 内部协议
- **不支持**：`/v1/audio/*`、`/v1/rerank`、`/v1/realtime`、`/v1/moderations`、`/v1/images/edits`、`/v1/edits`、Midjourney、Suno、Video、`/v1/completions`（旧 text completions）

### 3.4 协议覆盖差异表

| 入口 | TokenKey | New-API | 第五平台 `newapi` 桥接是否覆盖 |
|---|---|---|---|
| OpenAI Chat Completions | ✅ | ✅ | ✅（`bridge.DispatchChatCompletions`） |
| OpenAI Responses | ✅ | ✅ | ✅（`bridge.DispatchResponses`） |
| OpenAI Embeddings | ✅ | ✅ | ✅（`bridge.DispatchEmbeddings`） |
| OpenAI Images Generations | ✅ | ✅ | ✅（`bridge.DispatchImageGenerations`） |
| OpenAI Images Edits | ❌ | ✅ | ❌ |
| OpenAI Audio Speech (TTS) | ❌ | ✅ | ❌ |
| OpenAI Audio Transcription/Translation | ❌ | ✅ | ❌ |
| OpenAI Moderations | ❌ | ✅ | ❌ |
| OpenAI Realtime (WS) | ❌（仅 Responses WSv2 给 Codex 用） | ✅ | ❌ |
| OpenAI Rerank | ❌ | ✅ | ❌ |
| Anthropic `/v1/messages` 原生 | ✅（仅 anthropic 分组） | ✅ | ❌（newapi 分组的 `/v1/messages` 走 dispatch 翻译） |
| Gemini 原生 | ✅（仅 gemini 分组） | ✅ | ❌ |
| Midjourney 任务族 | ❌ | ✅ | ❌ |
| Suno 任务族 | ❌ | ✅ | ❌ |
| Video 任务族 | ❌ | ✅ | ❌ |

**协议覆盖缺口**：TokenKey + 第五平台 `newapi` 的总协议入口集合 = `OpenAI 五入口 + Anthropic /v1/messages + Gemini + Antigravity`，相当于 new-api 全集的约 1/3。剩余 2/3（音频、Realtime、Moderations、Rerank、Midjourney、Suno、Video）TokenKey 的当前架构原生不暴露，因为目标客户（Claude Code / cc switch / Cursor / Codex CLI）不需要这些。

---

## 4. 第五平台 `newapi` 的必要性判定

### 4.1 现状（事实陈述）

第五平台 `newapi` 已经在 v1.4.x（PR #9 → #19 → #29 → ...）完整 ship：
- 路由层：`tkOpenAICompat*` 已识别 `group.platform=newapi`
- 调度层：按 `group.platform` 分桶；`IsOpenAICompatPoolMember(groupPlatform)` 显式语义
- 桥接层：`internal/relay/bridge` 复用 new-api 的 `relay/channel/*` adaptor 与 `service/` 计费支持代码（无 GORM 操作）
- Admin UI：`AccountNewApiPlatformFields.vue`（base_url/api_key/channel_type/model_mapping/status_code_mapping/openai_organization）已并入 PR #19
- 计费链：复用 TokenKey 的 `RecordUsage`（用 `openAIUsageFromNewAPIDTO` 把 new-api 的 dto.Usage 翻译成 TokenKey 的 `OpenAIUsage`），所以**计费、订阅扣减、usage_log 写入完全走 TokenKey 链路**——bridge 只负责"打通上游协议 → 拿到 usage"
- 防漂移：`scripts/preflight.sh § 9/10` + `scripts/newapi-sentinels.json` 的 sentinel registry

### 4.2 必要性论证（为什么不能用 OpenAI(API Key) 直挂代替）

**论证 1：协议覆盖刚性差距**。`OpenAI + API Key` 直挂的上游必须**实质兼容 OpenAI Chat Completions 或 Responses schema**。下表是常见上游与"是否能纯 base_url 替换跑通"：

| 上游 | 兼容 OpenAI schema? | OpenAI(API Key) 直挂可行? | 第五平台 `newapi` (走 adaptor) 可行? |
|---|---|---|---|
| OpenAI 官方 / Azure（OpenAI deployment 模式） | ✅ | ✅ | ✅ |
| DeepSeek | 大体兼容 | ⚠️ 模型映射要手工补 | ✅（adaptor 内自动转） |
| Moonshot | 大体兼容 | ⚠️ 区域 base_url 易选错 | ✅（adaptor 自动 + Bug B 已修补 `MaybeResolveMoonshotBaseURLForNewAPI`） |
| Volcengine（火山引擎方舟） | 部分兼容 | ❌ 鉴权流不同 | ✅ |
| Baidu 千帆 | ❌ | ❌ | ✅ |
| Tencent 混元 | ❌ | ❌ | ✅ |
| Aws Bedrock | ❌（SigV4） | ❌ | ✅ |
| GCP Vertex | ❌（GCP IAM） | ❌ | ✅ |
| Anthropic 官方 API Key | 不同协议 | ❌（Anthropic 平台已有 oauth/setup_token，本身不暴露 api_key 通道给 openai 分组） | ✅（走 newapi adaptor 后能复用 TokenKey 调度） |
| Cohere / Mistral / xAI / OpenRouter | 部分/完全兼容 | ⚠️ ~ ✅ | ✅ |
| 国内 LLM 聚合（OhMyGPT / API2GPT / AILS / SiliconFlow / FastGPT） | 假装兼容（实际有差异） | ⚠️ 错误码体不一致，会触发 ErrorPassthrough 误报 | ✅（adaptor 已处理差异） |

**论证 2：TokenKey 自身订阅/账号治理是产品差异化的核心**。如果用户既想接 Volcengine/Vertex 等"OpenAI 协议不达标"的上游，又不想丢掉 TokenKey 的订阅扣费、API Key 中心化、usage_log 审计、账号粘性 sticky session、prompt cache 自动注入这些差异化能力——只能用第五平台 `newapi`：
- 直接独立部署 new-api 会丢掉 TokenKey 的订阅链路（new-api 没有 user_subscription）。
- 在 `OpenAI(API Key)` 路径里硬塞 channel_type 字段也跑不通（`base_url` 校验、协议假设全错）。

**论证 3：bridge 的成本仅一次性**。第五平台 `newapi` 的 bridge 复用 new-api 的 stateless 包，不引入 new-api 的 GORM/数据库依赖（CLAUDE.md 硬约束 §4）。维护成本主要是"上游 channel_type 接入新增时 sentinels 与 sync"，一次性成本远低于"在 TokenKey 里重写 30+ 个 adaptor"。

### 4.3 适用场景与禁用场景

**应使用第五平台 `newapi` 的场景（强建议）**：

1. 接入 **非 OpenAI 协议族** 的上游（Volcengine、Vertex、Bedrock、Baidu、Tencent、Cohere、Anthropic API Key 通道、Replicate、Dify…）。
2. 接入 **OpenAI compat 但有自家鉴权扩展** 的上游（Moonshot 区域差异、火山方舟 access_key 流、Coze 的 bot_id 注入、Azure deployment URL 模板…）。
3. 同一组合作伙伴提供"按 channel_type 分类"的多个密钥，希望按 channel 在 TokenKey 后台分别审计与限速。
4. TokenKey 主链客户（Claude Code / cc switch）希望兜底落到 newapi adaptor（例如主账号被限流、临时落到第三方 OpenAI 兼容池）。

**不应使用第五平台 `newapi` 的场景（强不建议）**：

1. 上游就是 **OpenAI 官方 API Key** 或 **完全兼容 OpenAI schema 的代理**（直挂 `OpenAI + API Key`，不要绕一层 bridge）——bridge 多一层 adaptor + dto 转换，徒增延迟与失败面。
2. 上游是 **OpenAI OAuth ChatGPT Team 账号**（必须走 `OpenAI + OAuth`，bridge 完全无关）。
3. 上游是 **Anthropic 官方 OAuth / setup_token**（走 `Anthropic + OAuth/setup_token`，bridge 无关）。
4. 上游是 **Gemini / Antigravity 平台原生**（走对应 platform，bridge 无关）。
5. 需要 **OpenAI 五入口之外的协议**（音频、Midjourney、Suno、Realtime、Rerank）——TokenKey 当前不接，需求大时应另起独立 design 决定是接入 bridge 新 endpoint，还是放弃此场景。

### 4.4 与 OpenAI(API Key) 的并存关系

| 决策点 | 选择 |
|---|---|
| 上游就是 OpenAI 协议族（含官方 + 第三方 OpenAI 代理）？ | 优先 `OpenAI + API Key` |
| 上游需要 OpenAI 官方 OAuth (ChatGPT Team)？ | 必须 `OpenAI + OAuth`（OAuth 入口） |
| 上游协议异构（百度/Vertex/Bedrock/Volcengine/Anthropic API Key）？ | 必须 `newapi + channel_type=N` |
| 想要 sticky session + prompt_cache 自动注入？ | 两条路都支持（newapi 走 `applyStickyToNewAPIBridge`） |
| 想要账号倍率/账号限速 + 订阅扣费？ | 两条路都支持（计费走 TokenKey `RecordUsage`） |
| 想要 OpenAI Responses WSv2？ | 仅 `OpenAI + API Key`（newapi adaptor 不接 WSv2） |
| 想要 admin 直接看到上游错误码定制透传？ | 两条路都支持（`status_code_mapping` newapi 通过 credentials 暴露） |

### 4.5 风险与边界（已知缺陷与延期项）

来自 `docs/approved/newapi-as-fifth-platform.md` §11.5 / §6 与 follow-up 设计：

- 端到端 e2e（HTTP+PG+upstream）测试未覆盖（mock 单测已覆盖核心 AC）；上线前需要灰度验证。
- bridge 不接 Midjourney / Suno / Realtime / Audio / Rerank / Moderations，与 new-api 全集仍有 ~2/3 协议入口缺口，本设计明确不补。
- `OpenAI` 与 `newapi` 的调度池被严格隔离（`group.platform` 决定），**不允许混挂**。这避免了"误调度到 channel_type=0 的 OpenAI 账号"这类 P0 漏洞，但也意味着如果业务希望"OpenAI 官方 + 第三方代理 + 自家 LLM"在同一个 group 里被自动 model→channel 路由，TokenKey 现在不支持——必须按上游协议分组。
- ErrorPassthrough 与 newapi 错误体的对接已在 PR #29 收口，但每接入一种新 channel_type 仍需要确认错误格式不被吞。
- 计费 `web_search_call_count / file_search_call_count / audio_input_price` 等 new-api 扩展字段尚未流到 TokenKey usage_log；如果接入"OpenAI 官方 web_search 工具调用"类场景，会少计费。

### 4.6 必要性结论

**保留并继续投入第五平台 `newapi`，必要性高**。
- 它是 TokenKey 在不破坏"订阅 / 账号 / 分组"治理模型前提下，**唯一**能覆盖 OpenAI 协议族之外上游的接入点。
- 它的实施成本是一次性的 bridge + sentinel 维护，远低于"在 TokenKey 里手写 30+ 个 adaptor"。
- 它和 OpenAI(API Key) 直挂**职责互补**：直挂负责"上游就是 OpenAI 协议"的快路径；newapi 负责"上游协议异构"的慢但通用路径。

**不该继续扩展的方向**：
- 在 newapi 上加 Midjourney/Suno/Realtime/Audio/Rerank 入口——这会让 TokenKey 偏离"Claude Code / cc switch 主链 SaaS 网关"的产品定位，向"通用 LLM 聚合代理"漂移；如果有真实需求，应该单独立项重新评估，而不是默认从 bridge 顺手补齐。
- 在 newapi 分组里允许"混挂多种 channel_type 自动按 model 路由"——这破坏了 TokenKey 的强类型分组语义，会引入"哪个 channel 出问题、哪个被扣费"的可观测性塌方。

---

## 5. 给运维 / 产品 / 架构的可执行建议

### 5.1 后台 UI 表单文案应增加"如何选"指引

| 选项 | 表单旁应有 hint |
|---|---|
| OpenAI + API Key | "上游是 OpenAI 官方 API、Azure OpenAI、或与 OpenAI Chat Completions/Responses **完全兼容**的代理时使用。base_url 不写则默认 `https://api.openai.com`。" |
| OpenAI + OAuth | "把 ChatGPT Plus/Team 订阅账号转 API 时使用。仅此入口可走 `chatgpt.com/backend-api`。" |
| New API（第五平台） | "上游是 DeepSeek / Moonshot / Volcengine / Vertex / Bedrock / Baidu / 国产模型 / 任意 OpenAI 兼容代理 时使用。需要选择 channel_type 来决定上游协议适配器。" |

### 5.2 文档侧的修补点

- `docs/flows/newapi-messages-gateway-flow.md` 已存在，应补一个对应物 `docs/flows/openai-apikey-gateway-flow.md`（当前只有 `openai-gateway-flow.md` 偏 OAuth 版本）。
- `README` 中"Add Account"章节应更新选择决策树（基于 §4.4 的表格）。

### 5.3 架构层不应做的事（避免漂移）

- 禁止把 `OpenAI(API Key)` 路径里的 `base_url` 用作"伪 channel adaptor"——一旦上游不是真 OpenAI 协议，错误体、模型名、限流头都会失配，会被 `ErrorPassthroughService` 误报或被 `RateLimitService` 错误暂停。这种"想偷懒不走 newapi"的做法会污染调度状态。
- 禁止在 newapi 分组里"开混挂"（让 channel_type 不同的账号共存于一个 group 的同一 model 路由）——这会让 sticky session 漂移、prompt cache 失效、统计维度模糊。

### 5.4 后续设计应回答的开放问题（不在本文档解决）

1. 是否要把 new-api 的 `model_ratio` 表与 TokenKey 的 model-pricing resolver 对齐？目前两套表演化，存在第五平台账号定价"两份事实"的隐患。
2. 是否要把 `OpenAI(API Key)` 入口的 base_url 校验补 "尝试 fetch /v1/models 验证协议兼容"的 connect-test？现在用户输错 base_url 只能在第一次真实请求时才暴露。
3. 是否要把 newapi 分组的 sticky session key 按 `channel_type` 拆？现在是按 `(groupID, sessionHash)`，不同 channel_type 共享 key；如果同一 group 真的混挂（虽然§4.6 反对），会产生 sticky 漂移。

---

## 附录 A：关键代码索引

| 主题 | 文件 | 关键符号 |
|---|---|---|
| OpenAI(API Key) 上游 URL 推导 | `backend/internal/service/openai_gateway_service.go` | `buildUpstreamRequestOpenAIPassthrough` / `forwardOpenAIPassthrough` |
| OpenAI(API Key) base_url 校验 | `backend/internal/service/openai_gateway_service.go` | `validateUpstreamBaseURL` |
| OpenAI account credential getters | `backend/internal/service/account.go` | `GetOpenAIBaseURL`, `GetOpenAIApiKey`, `GetOpenAIAccessToken` |
| 第五平台 newapi 桥接入口 | `backend/internal/service/openai_gateway_bridge_dispatch.go` | `ShouldDispatchToNewAPIBridge`, `ForwardAs*Dispatched` |
| bridge → new-api adaptor | `backend/internal/relay/bridge/dispatch.go` | `DispatchChatCompletions/Responses/Embeddings/ImageGenerations` |
| channel_type 目录服务 | `backend/internal/integration/newapi/channel_types.go` | `ListChannelTypes` |
| channel_type → 默认模型 | `backend/internal/integration/newapi/channel_type_models.go` | `ChannelTypeModels` |
| 调度池语义（按 group.platform 分桶） | `backend/internal/service/account_tk_compat_pool.go` | `IsOpenAICompatPoolMember`, `OpenAICompatPlatforms`, `AllSchedulingPlatforms` |
| messages dispatch 净化 | `backend/internal/service/openai_messages_dispatch_tk_newapi.go` | `isOpenAICompatPlatformGroup` |
| Moonshot 区域解析（newapi 专用） | `backend/internal/integration/newapi/moonshot_resolve_save.go` | `MaybeResolveMoonshotBaseURLForNewAPI`, `ResolveMoonshotRegionalBaseAtSave` |
| TokenKey 计费统一入口 | `backend/internal/service/billing_service.go` | `CalculateCostUnified`, `calculateTokenCost`, `calculatePerRequestCost` |
| OpenAI 网关 RecordUsage | `backend/internal/service/openai_gateway_service.go` | `RecordUsage`, `OpenAIRecordUsageInput` |
| New-API 计费 | `/new-api/service/billing.go` `/new-api/service/billing_session.go` `/new-api/service/text_quota.go` | `PreConsumeBilling`, `SettleBilling`, `calculateTextQuotaSummary` |
| New-API channel_type → api_type | `/new-api/common/api_type.go` | `ChannelType2APIType` |
| New-API channel 名称表 | `/new-api/constant/channel.go` | `ChannelType*`, `ChannelTypeNames` |
| New-API RelayMode | `/new-api/relay/constant/relay_mode.go` | `Path2RelayMode` |
| Sentinel 防漂移 | `scripts/newapi-sentinels.json` `scripts/check-newapi-sentinels.py` | — |

## 附录 B：术语对照表（TokenKey ↔ New-API）

| TokenKey | New-API | 备注 |
|---|---|---|
| Account | Channel | 都是"上游接入实体"，但 TokenKey 强类型 platform、new-api 弱类型 channel_type |
| Group | Group | TokenKey 强 platform；new-api 仅做 ratio 隔离 |
| API Key | Token | 都是下游凭据；TokenKey API Key 进而绑定订阅 |
| user_subscription | （无） | 只在 TokenKey 存在 |
| platform | （无显式概念） | TokenKey 的强类型分桶不在 new-api |
| channel_type | channel_type | newapi 第五平台账号沿用 new-api 的 channel_type 值 |
| usage_log | logs.consume_log | 字段不同，但语义对齐 |
| BillingService.CalculateCostUnified | service.PreConsumeBilling + SettleBilling | 后置 vs 预扣 |
| ScheduleSnapshot.bucketKey | distribute() + channel.priority | 两边都做"在候选里挑一个" |
