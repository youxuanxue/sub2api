# TokenKey 全平台可服务模型清单（All-Platform Model Inventory）

> **用途**：把「TokenKey 在 7 个平台上到底能服务哪些模型、各自定价/展示/广告状态如何、哪些实测不可服务、为什么不在清单里、以后还要不要再试」一次性写清楚，作为权威参考，避免反复实测与「这模型为什么没了」的反复追问。
>
> **数据来源（repo-grounded，非线上探测）**：本清单由仓库内的权威源推导——5 个 Go servable-allowlist map、`tk_served_models.json` 清单、`tk_pricing_overlay.json` 价格 overlay、各平台 `DefaultModels`、newapi 渠道适配器目录、`model_mapping` 迁移。
>
> **快照日期**：2026-06-21 抓取，2026-06-22 更新 openai/grok/antigravity-tab/GLM 处理状态，2026-06-23 复测 openai/gemini/antigravity/grok/newapi watchlist。实测探针基线：claude/gpt 2026-06-05（`codex-auto-review` 2026-06-23 responses 200）、gemini/Vertex 2026-06-09（2026-06-23 复测遇到基线同为 429，判不可定）、antigravity 2026-06-23、grok 2026-06-22（2026-06-23 alias 再探 200）、VolcEngine Ark chat 2026-06-23。**point-in-time 状态会过期**——带 `transient` 标记的条目必须按 §4 的 reprobe-watchlist 定期复核，不能当永久结论。
>
> **如何重生成渠道目录**（附录 A）：在 `backend/` 写一个 `//go:build unit` 临时 test 调 `newapi.ChannelTypeModels()` / `ListChannelTypes()` 打印即可（本清单即如此抓取，用后删除）。

---

## 0. TL;DR

TokenKey 的完整目录是一个**四层洋葱**，几乎每个「缺口」都是相邻两层对同一个模型的判断不一致：

```
PRICED（有非零价，~140 id，最宽）
  ⊇ SERVABLE（网关真能拿到 200）
      ⊇ DISPLAYED（公开 /pricing storefront 展示）
ADVERTISED（在某平台 DefaultModels → 喂 /v1/models 与「我的菜单」）  ← 与上面三层正交
```

- **7 个平台**：anthropic / openai / gemini / antigravity（前四原生）+ **newapi**（第五，OpenAI 兼容长尾）+ **kiro**（第六，CodeWhisperer 中继）+ **grok**（第七，xAI OAuth 中继）。
- **原生 servable allowlist 数量**：anthropic 8、openai 16、gemini 7、antigravity 10、grok 8（5 个 Go map）。
- **newapi 经账号 `model_mapping` 服务的策展长尾**：qwen/deepseek 在账号 60/39，VolcEngine/doubao/seedream/seedance 在账号 7（2026-06-23 Ark chat 19 个 200 已补 manifest；`doubao-seed-translation-250915` 400 不进清单），GLM 直连族在账号 67（tk_044，prod canary 2026-06-22 已 livefire 200 + 计费核账）。
- **总计**：约 110 个 servable id / 140 个 priced id。
- **不可服务台账**：机器源在 `ops/pricing/servable-reprobe-ledger.json`，当前分为 watchlist / skiplist / deadlist；不要维护手写总数。

**三大风险类（按严重度）**：

| 类别 | 是什么 | 现状 | 处置 |
|---|---|---|---|
| `servable_unpriced`（chat） | 可服务但无价 → 计 `$0` 无扣额 | **会发 P0 告警，不是 silent**（`served_zero_cost` 探针）。本轮已处理主例：grok-4.3/4.20/build/code-fast 官方价进 overlay+allowlist，antigravity `tab_flash_lite_preview` 从默认/mapping 自愈面移除 | 新发现条目需补**官方**核实价进 overlay → 加 allowlist；或从 defaults/mapping 移除。**不要给 chat 加 fail-closed 守卫**（见下） |
| `advertised_dead` | 在 `DefaultModels` 但实测 502/404 | 客户能在 /v1/models 或菜单里选到打不通的模型。OpenAI 侧已改为统一 servable+priced fallback：`codex-auto-review` 2026-06-22 实测 200 后保留，`gpt-5.2`、`gpt-5.3-codex`、`gpt-image-*` 不再进入默认面。gemini-2.0-flash、gemini-3.x chat 仍按 project-scoped watchlist 管理 | servable-refresh 复测确认 200 则留，否则从可见面移除，并用同一 allowlist 闸 `DefaultModels` |
| `channel_not_onboarded` | 渠道适配器理论可达但无 TK 账号/价 | 扩展 backlog，非缺陷。openai 153+24 尾、gemini ct24/41、Moonshot/MiniMax/Zhipu… | 有客户需求时走 `tokenkey-onboard-model` 逐个上架 |

**一个刻意的非对称（不要误判为缺陷）**：`media` 路径（image/video）对无价模型**先拒后服务**返回 400（一条视频上游可达 ~$22，硬失败防资损）；`chat` 路径**先服务后告警**（一条 chat 是分级成本，可用性优先，靠 `served_zero_cost` P0 兜底）。这是操作员 2026-06-12 拍板的成本加权决策（`openai_gateway_service_tk_media_unpriced_guard.go` 头注），**不是缺的守卫**。

---

## 1. 架构：7 平台 × 4 个状态维度

### 1.1 平台拓扑

| # | 平台 `platform` | 上游 | 调度/中继方式 | servable 来源 |
|---|---|---|---|---|
| 1 | `anthropic` | Anthropic OAuth | 原生；prod→edge 镜像中继（`cc-<edge>` apikey）；账号级 sticky/load-aware | Go `supportedAnthropicCatalogModels`（硬闸）|
| 2 | `openai` | OpenAI / Codex OAuth | 原生；GPT 专线 key | Go `supportedOpenAICatalogModels`（硬闸）|
| 3 | `gemini` | Google Vertex AI | 原生；media 经 Vertex ch41 | Go `supportedGeminiCatalogModels`（空则透传）|
| 4 | `antigravity` | Google cloudcode-pa OAuth | 原生中继；**仅服务 gemini**（claude 路由到 anthropic，gpt-oss 排除）| Go `supportedAntigravityCatalogModels`（空则透传）|
| 5 | `newapi` | 各 channel 上游（Ali/DeepSeek/VolcEngine/…）| OpenAI 兼容网关 + new-api 适配器（`channel_type>0`）| 账号 `credentials.model_mapping` 白名单 + `tk_served_models.json` 清单（**无 Go map**，靠价格存在透传）|
| 6 | `kiro` | AWS CodeWhisperer | prod→edge anthropic apikey 中继；镜像进 claude 组 | 中继 claude id（无自有目录）|
| 7 | `grok` | xAI（SuperGrok Heavy OAuth）| 原生 OAuth 中继；chat/image/video 全臂 | Go `supportedGrokCatalogModels`（空则透传）|

### 1.2 四个状态维度（每个模型都要分开看）

- **SERVABLE**：网关能真拿到 200。判定方式按平台不同：原生显示平台是 Go 硬 allowlist；newapi 是 per-account `model_mapping` identity 白名单（空 mapping = 该账号 catch-all 放行全渠道）；kiro 与 grok 原生臂是纯透传中继。
- **PRICED**：在 litellm 运行时镜像或 `tk_pricing_overlay.json` 有非零价。`price_source ∈ {overlay, mirror, channel, none}`。`none` = 计 `$0`（chat 会 P0 告警；media 会被 400 守卫拦下）。
- **DISPLAYED**：是否进 `GET /api/v1/public/pricing`，由 `isPublicCatalogModelSupported` 决定（`pricing_catalog_supported_models_tk.go`）。
- **ADVERTISED**：是否在某平台 `DefaultModels`（喂网关 `/v1/models` 与「我的菜单」）。**与可服务正交**——可服务未必广告（如 `claude-opus-4-1`），广告未必可服务（`advertised_dead`）。

### 1.3 公开目录闸门逻辑（`isPublicCatalogModelSupported`）

```
anthropic / openai → 硬闸：只放 allowlist 内的 id（map 永不为空）
gemini / antigravity / grok → 软闸：map 为空时透传（不收窄，零回归）；非空则像 claude/gpt 一样收窄
newapi（dashscope/deepseek/volcengine vendor）→ inferPlatformFromVendor 不识别 → default-true 透传，靠「价格存在」隐式收窄
```

> 推论：**`DefaultModels`（/v1/models 来源）与 /pricing storefront 用的是两套闸**——前者按 `IsModelPriced`（全镜像），后者按 servable-allowlist。两者不一致正是 `advertised_dead` 类缺口的根因。

---

## 2. 逐平台可服务清单（正面）

> 状态列含义：`A`=in servable allowlist / `M`=account model_mapping / `P`=passthrough；价：`mirror`/`overlay`/`none`；`disp`=公开目录展示；`adv`=DefaultModels 广告。

### 2.1 anthropic（claude，第一平台）

servable allowlist 共 **8** 个 bare canonical id（`pricing_catalog_supported_models_tk.go`，gofmt 字母序）：

| model_id | servable | 价 | disp | adv | 备注 |
|---|---|---|---|---|---|
| `claude-opus-4-8` | A | mirror | ✓ | ✓ | 当前 Opus 旗舰 |
| `claude-opus-4-7` | A | mirror | ✓ | ✓ | + `-max/-xhigh/-high/-medium/-low/-thinking` effort 变体仅在 newapi bridge |
| `claude-opus-4-6` | A | mirror | ✓ | ✓ | dated `-20260205` / `-thinking` 仅镜像，不在 allowlist |
| `claude-opus-4-5` | A | mirror | ✓ | ✓(dated) | DefaultModels 用 dated `-20251101`；`ModelIDOverrides` bare→dated |
| `claude-opus-4-1` | A | mirror | ✓ | **✗** | 在 allowlist+公开目录，但**不在 DefaultModels** → /v1/messages 不列、需显式请求；价 $15/$75（legacy） |
| `claude-sonnet-4-6` | A | mirror | ✓ | ✓ | 当前 Sonnet；有 per-class(sonnet) 冷却窗（#916）|
| `claude-sonnet-4-5` | A | mirror | ✓ | ✓(dated) | DefaultModels 用 dated `-20250929`（也是 `DefaultTestModel`）|
| `claude-haiku-4-5` | A | mirror | ✓ | ✓(dated) | 最便宜档；dated `-20251001`；有 Haiku 专属 mimicry beta |

- **canonical/dated 分裂已完全处理**：`ModelIDOverrides`（bare→dated 上行）/`ModelIDReverseOverrides`（上游 id→bare 计费键），两形都在镜像里有价。
- **`claude-fable-5`**：原生 anthropic **已不可服务**（2026-06-13 起 404 access-gated）——见 §4。注意 antigravity 仍保留自己的 fable-5（per-platform 真值）。
- **overlay 只有 1 个 anthropic 键**（`claude-fable-5`）；其余所有 claude 计费键（legacy、dated、`-thinking`、8 个 servable bare）都在 **litellm 镜像**里，不在 overlay。
- **多路由可达（cross-platform）**：同一批 claude id 还能经 kiro 中继（按请求 id 计费、token 估算）、newapi bridge ct=14/33/41（per-account mapping，`-thinking/-effort` 变体）到达——价格解析与 token 计数路径各不相同，详见 §3 与 §2.7。

### 2.2 openai（gpt/codex，第二平台）

servable allowlist 共 **16**：

```
codex-auto-review
gpt-5  gpt-5-chat  gpt-5-chat-latest  gpt-5-mini  gpt-5-nano  gpt-5-pro  gpt-5-search-api
gpt-5.1  gpt-5.1-chat-latest  gpt-5.3-codex-spark  gpt-5.4  gpt-5.4-mini  gpt-5.4-pro
gpt-5.5  gpt-5.5-pro
```

- 价全部来自 **litellm 镜像**（overlay 无 openai 条目）。
- **`advertised_dead` 收敛结果（2026-06-22）**：`codex-auto-review` 经 `/v1/responses` 实测 200，已进 openai allowlist；`gpt-5.2`（400 unsupported）、`gpt-5.3-codex`（400 unsupported）、`gpt-image-1`/`gpt-image-1.5`/`gpt-image-2`（原生 OAuth 结构性做不了图，需 `type=apikey` 账号）不再进入 /v1/models fallback 或 admin 默认候选。
- **codex 形** 走 `/v1/responses`；`codex-mini-latest` 被 codex normalization 重计为 `gpt-5.3-codex` 才免于 $0。
- **channel 长尾**：ct=1（153 模型：o1/o3/o4、gpt-4*/4o*、audio/realtime/tts、embeddings、dall-e、sora-2…）与 ct=57 codex 订阅（24）**均未经原生 openai 平台服务**——是 newapi bridge 的扩展 backlog（§5）。

### 2.3 gemini / Vertex（第三平台）

servable allowlist 共 **7**（2026-06-09 探针）：

| model_id | mode | 价 |
|---|---|---|
| `gemini-2.5-flash` / `-flash-lite` / `gemini-2.5-pro` | chat | mirror |
| `imagen-4.0-fast-generate-001` / `-generate-001` / `-ultra-generate-001` | image | overlay(vertex_ai) |
| `veo-3.1-generate-001` | video | overlay(vertex_ai) |

- **`priced_not_displayed`（媒体，~11，低危）**：overlay 里还有 `imagen-3.0-*`（4 个）、`veo-2.0/3.0/3.1` 多个变体——**有价但不在 7-id 展示闸**。2026-06-23 edge-us6 走正确 image/video 端点复测，全部返回 429 inconclusive，不能当成已支持；已进入 `servable-reprobe-ledger.json` watchlist，等下次拿到 200 再扩 allowlist。
- **`advertised_dead`**：`gemini-2.0-flash`（也是 admin `geminicli.DefaultTestModel`）、`gemini-3.x` chat——2026-06-09 在该 Vertex project 统一 502（**project/region 级**，非 vendor 级：同 wire id 在 antigravity 能 200）。2026-06-23 复测这些 id 与 `gemini-2.5-flash` 基线同返 429，当前只能判为池/配额不可定，不能迁成永久不支持。
- **wrong-surface 陷阱**：`gemini-*-image`（`gemini-2.5-flash-image` 等）经 `/v1/images/generations` 探返 500，但它们其实走 **chat 端点**——是**无效探针**不是模型死了。2026-06-23 改走 `/v1/chat/completions` 后与 `gemini-2.5-flash` 基线同返 429，继续留在 watchlist。
- media 路由经 Vertex ch41；gemini 原生生图走 `/v1/chat/completions` 返 markdown 图。

### 2.4 antigravity（第四平台，仅 gemini）

servable allowlist 共 **10**（hand-maintained，2026-06-13 探针；2026-06-23 专项复测闭环）。账号 mapping 实服 ~14 个 gemini wire id。2026-06-23 专项复测确认 `gemini-2.5-flash`、`gemini-2.5-flash-lite`、`gemini-2.5-flash-thinking`、`gemini-3-flash` 以及既有基线 `gemini-3-flash-agent`、`gemini-3.1-pro-low`、`gemini-pro-agent`、`gemini-3.1-flash-image` 均能经 prod `/antigravity/v1beta/...:generateContent` 返回 200；`gemini-2.5-pro` 在 generateContent 与 streamGenerateContent 单模型复测均为 000 timeout/inconclusive，继续 watch。

```
gemini-2.5-flash           gemini-2.5-flash-lite    gemini-2.5-flash-thinking
gemini-3-flash             gemini-3-flash-agent     gemini-3.1-flash-image
gemini-3.1-pro-low         gemini-3.5-flash-extra-low
gemini-3.5-flash-low       gemini-pro-agent
```

- 价/展示闭环（2026-06-23）：`gemini-2.5-flash-thinking` 已补 `tk_pricing_overlay.json`（按 bundled `gemini-2.5-flash` 官方价镜像：in $0.30/M、out $2.50/M、cache-read $0.03/M）；`gemini-3-flash-agent`、`gemini-3.5-flash-{low,extra-low}`、`gemini-pro-agent` 继续走 Antigravity overlay；`gemini-2.5-flash`、`gemini-2.5-flash-lite`、`gemini-3-flash`、`gemini-3.1-flash-image`、`gemini-3.1-pro-low` 走 bundled/litellm Gemini/Vertex 非零价。`/antigravity/models` 和 admin selector 已接 `supportedAntigravityCatalogModels`，因此这些 10 个 id 会作为 Antigravity 默认可见候选；`gemini-2.5-pro` 虽有原生 Gemini 价，但因 Antigravity 复测未拿到 200，不进该面。
- `/api/v1/public/pricing` 仍是 flat `model_id` 目录：同名模型（如 `gemini-3-flash`）已有 Gemini/Vertex vendor 行时，fill-only overlay 不改 vendor 归属；只有 overlay-only wire id（如 `gemini-2.5-flash-thinking`、`gemini-3-flash-agent`、`gemini-pro-agent`）会显示为 `vendor=antigravity`。这是当前 DTO 的平台维度限制，不影响 Antigravity 请求按 `requested_model` 计费。
- **`tab_flash_lite_preview` 清理（2026-06-22）**：该模型无公开价，已从默认 antigravity mapping / gemini-only mapping / reconciler 目标面移除，并由静态检查标为 unpriced mapping violation，避免继续可见或自愈回写。
- **policy（不可服务因策略）**：整个 claude-* 家族 + `gpt-oss-120b-medium` 按操作员策略不在 antigravity 服务（claude 路由到 anthropic）；由 `AntigravityConfigReconciler` 自愈维持 gemini-only。2026-06-23 `claude-sonnet-4-5` 经 `/antigravity/v1/messages` 返回 429 not_allowlisted，符合当前策略。

### 2.5 grok（第七平台，xAI）

servable allowlist 共 **8**（与公开目录、overlay xai 同源）：

| model_id | mode | 价(overlay xai) | failure_billing |
|---|---|---|---|
| `grok-code-fast-1` | chat | $0.20/$1.50 /Mtok | — |
| `grok-build-0.1` | chat | $1/$2 /Mtok | — |
| `grok-4.3` | chat | $1.25/$2.50 /Mtok | — |
| `grok-4.20-0309-reasoning` / `-non-reasoning` | chat | $1.25/$2.50 /Mtok | — |
| `grok-imagine-image` | image | $0.02/img | — |
| `grok-imagine-image-quality` | image | $0.07/img(2K 保守档) | — |
| `grok-imagine-video` | video | $0.08/s(720p+img 上限档) | success_only |

- **2026-06-22 收敛**：`grok-4.3`、`grok-4.20-0309-*`、`grok-build-0.1`、`grok-code-fast-1` 已用 docs.x.ai 官方价补 overlay，并经 edge-us4 原生 grok 探针实测 200 后进入 allowlist。未官方定价或未 200 的 grok-3 / grok-2-vision / search 变体仍保持 `policy` 排除，不臆造价格。
- **兼容别名（不公开列）**：`grok-4.3-latest`、`grok-latest`、`grok-4-fast-reasoning`、`grok-code-fast`、`grok-code-fast-1-0825` 均有 overlay 价，2026-06-23 edge-us4 原生 grok 探针复测 200；它们只保证显式请求不落 `$0`，公开目录仍只列稳定 bare id / 当前官方 SKU。
- 视频原生异步臂（submit/poll），`expired` 故意非终态防退款资损。
- 原生 grok 臂 与 newapi ch48 聚合中继是两条到 xAI 的不同路径。prod→edge grok 中继长期收敛为 `platform=grok,type=apikey` relay；旧 `newapi` edge-host bridge 仅作为迁移兼容形态保留。

### 2.6 newapi（第五平台）—— 策展长尾

**无 Go map**，靠账号 `model_mapping` 白名单 + 价格存在透传；`display=false`。意图源 = `tk_served_models.json`。

**(a) Qwen（账号 60，ct=17 Ali/DashScope，group 18）+ DeepSeek（账号 39，ct=43，group 11）**

| 家族 | servable id（account_mapping）| 价 |
|---|---|---|
| Qwen 商用 | `qwen3.7-max` `qwen3.7-max-preview` `qwen3.7-max-2026-05-17` `qwen3.7-max-2026-05-20` `qwen3.7-max-2026-06-08` `qwen3.7-plus` `qwen3.6-flash` `qwen3-coder-plus` `qwen-max` `qwen-turbo` `qwen-plus` | overlay(dashscope) |
| Qwen 开源 dense | `qwen3-8b` `qwen3-14b` `qwen3-32b` `qwen3.6-27b`（tk_039）`qwen3-235b-a22b` | overlay（思考/非思考双档）|
| DeepSeek | `deepseek-v4-pro` `deepseek-v4-flash` | overlay |
| DeepSeek 经典别名 | `deepseek-chat` `deepseek-reasoner` | **mirror**（overlay 故意不收，镜像已带非零价）|

- **`priced_not_served` 错配（中危）**：`qwen2.5-coder-32b` / `qwen2.5-coder-7b` —— **有价但不在账号 60 mapping**（parity 行），却因 dashscope vendor 走 default-true 而**展示**在 /pricing，请求空池快失败 429。`qwen3.7-max-preview` 与 `qwen3.7-max-2026-05-17/-05-20/-06-08` 已在 2026-06-23 prod mapping 中确认存在，thinking 路径实测 200（05-20/06-08 非 thinking 也 200；preview/05-17 非 thinking 为请求形状 400，不代表不可服务）。`qwen2.5-coder-*` 存在是为闭合一条客户-channel 漏算（`qwen2.5-coder→gpt-5.4` ~$269 低估），属计费键 parity，非给客户调。

**(b) VolcEngine / Doubao + 媒体（账号 7，ct=45）**

overlay `litellm_provider="volcengine"` 共 28 条；`tk_served_models.json` 当前正面清单是账号 7 的 24 条（19 chat + 1 image + 4 video），都必须同时满足 mapping + overlay + 实测/既有 served 证据。

| mode | servable id（manifest/account_mapping）|
|---|---|
| chat | `doubao-seed-2-0-pro-260215`、`doubao-seed-2-0-code-preview-260215`、`doubao-seed-2-0-lite-260215`、`doubao-seed-2-0-lite-260428`、`doubao-seed-2-0-mini-260215`、`doubao-seed-2-0-mini-260428` |
| chat | `doubao-seed-1-8-251228`、`doubao-seed-1-6-250615`、`doubao-seed-1-6-251015`、`doubao-seed-1-6-flash-250615`、`doubao-seed-1-6-flash-250828`、`doubao-seed-1-6-vision-250815` |
| chat | `doubao-seed-character-251128`、`doubao-seed-code-preview-251028`、`doubao-1-5-pro-32k-250115`、`doubao-1-5-pro-32k-character-250715`、`doubao-1-5-lite-32k-250115`、`doubao-1-5-vision-pro-32k-250115`、`glm-4-7-251222` |
| image | `doubao-seedream-4-0-250828`（no-prefix `seedream-4-0-250828` 只是 parity 计费键，不进 manifest）|
| video | `doubao-seedance-1-0-pro-250528`、`doubao-seedance-1-5-pro-251215`、`doubao-seedance-2-0-260128`、`doubao-seedance-2-0-fast-260128`（no-prefix `seedance-1-0-pro-*` 只是 parity 计费键）；`failure_billing=success_only` |

> `deepseek-v3-2-251201` 在 overlay 标 volcengine 但 **tk_020 故意不在账号 7 服务**（VolcEngine 自报价 ~4× 官方 DeepSeek 价）；其 servable 家在 DeepSeek 直连（账号 39）。volcengine 标签价是无害残留。`doubao-seed-translation-250915` 虽在 tk_020 mapping 与 overlay 中，但 2026-06-23 两次 direct Ark `/api/v3/chat/completions` 复测均为 400 inconclusive，本轮不进 manifest/正面清单。

- 媒体类的 `servable_unpriced` 风险全被 **media 400 守卫**收口为干净报错，无资损。
- 故意排除的上游媒体变体（`seedream-4.5/5.0(-lite)`、`seedance-1.0-pro-fast`、`seedance-1.0-lite`）见 §4/§5。

**(c) GLM 直连（账号 67，ct=26 ZhipuV4，group 26/GLM）**

tk_044 把 GLM 从 legacy ct16 Zhipu v3 迁到 ct26 ZhipuV4/OpenAI-compatible，并把 `base_url` 规范为 `https://open.bigmodel.cn`（适配器自接 `/api/paas/v4`）。官方 paid SKU 已进 overlay + manifest：

```
glm-5.2  glm-5.1  glm-5  glm-5-turbo
glm-4.7  glm-4.7-flashx  glm-4.6
glm-4.5  glm-4.5-x  glm-4.5-air  glm-4.5-airx
glm-4-32b-0414-128k
```

free SKU `glm-4.7-flash` / `glm-4.5-flash` 刻意不进 `model_mapping` / overlay 公开目录，避免可见 `$0` 模型。prod canary 已于 2026-06-22 完成：runtime overlay 热推后，account 67 切到 ct26、`base_url=https://open.bigmodel.cn`、12 个 paid mapping；`ZHIPU_CHAT_MODELS=glm-4.7` 经 prod 网关返回 `200 servable`，`usage_logs` 落 account 67 / group 26 且 `total_cost=0.0004964000` 非零。

### 2.7 kiro（第六平台，CodeWhisperer 中继）

- **无自有目录、无 overlay 价**——纯**中继 claude 请求**到 CodeWhisperer，prod→edge anthropic apikey 拓扑。
- 客户面 claude id 复用 §2.1 的 anthropic 可服务集；按**请求 id** 计费（`billing_tier=kiro-estimated`，因 CodeWhisperer 不返 token usage、parser 得 (0,0) → TK 估算 token）。
- **dot-form 计费风险已按代码事实证伪**：Kiro `MapModel` 会把 dash 版 Claude id 规范成 dot-form（如 `claude-sonnet-4.5`）用于上游；Anthropic 计费 funnel 的默认 billing key 是 `ForwardResult.Model`（请求 id）。无论请求 id 是 dash-form 还是 dot-form，`PricingService.matchByModelFamily` 都能匹配 `claude-sonnet-4-5` / `claude-sonnet-4.5` 并命中 dash/dated 镜像价，不会因拼写差异落 `$0`。回归测试：`TestCalculateCost_ClaudeDotFormMatchesDashFormFamilyPricing`。
- 429=空池（在 toggle/上游之前）；502=disabled 或上游拍平无 failover。

---

## 3. 跨平台缺口分析（gap）

> 已应用对抗复核修正：**(1)** unpriced-chat 的 $0 是「**会 P0 告警**」不是 silent；**(2)** 不建议给 chat 加 fail-closed 守卫（反转 2026-06-12 操作员决策）。

| kind | 平台 | 代表模型 | 危 | 处置 |
|---|---|---|---|---|
| `servable_unpriced_zero_cost_p0` | grok | grok-3/2/search 未核价长尾 | 中 | grok-4.3/4.20/build/code-fast 已补官方价+allowlist；剩余项继续 leave_excluded，等官方价+200 |
| `servable_unpriced_zero_cost_p0` | antigravity | `tab_flash_lite_preview` | 已收敛 | 已从默认/mapping 自愈面移除，静态检查阻止回写 |
| `advertised_dead` | openai | `gpt-5.2` `gpt-5.3-codex` `gpt-image-{1,1.5,2}` | 已收敛 | `codex-auto-review` 实测 200 后加入；其余死项不再进默认可见面 |
| `advertised_dead` | gemini | `gemini-2.0-flash`（含 admin 测试默认）`gemini-3.x` chat | 中 | 复测；用 servable-allowlist 闸 DefaultModels |
| `channel_not_onboarded` | openai/gemini/newapi | ct1/57、ct24/41、Moonshot/MiniMax/Zhipu… | 中 | 见 §5 backlog |
| `priced_not_displayed` | gemini/antigravity/volcengine | imagen-3.0/veo 变体、gemini-3.1-pro-low、deepseek-v3-2 | 低 | Gemini media 2026-06-23 复测 429，留 watchlist；deepseek-v3-2 是 VolcEngine 价残留，账号 7 不服务 |
| `priced_mapped_not_proven_served` | newapi(volcengine) | `doubao-seed-translation-250915` | 中 | tk_020 mapping + overlay 已有，但 2026-06-23 direct Ark 两次 400 inconclusive；不进 manifest，留 watchlist |
| `priced_not_served` | newapi(qwen) | qwen2.5-coder-* | 中 | 抑制 parity 行的展示，或在账号 60 真 mapping；qwen3.7-max preview/dated 已由 2026-06-23 prod mapping + livefire 证实可服务 |
| `dated_dup` | anthropic/grok/volcengine | claude bare↔dated、grok-imagine-image-pro、no-prefix seedream/seedance | 低 | anthropic 已由 override 机制处理；其余被 media 守卫/上游 404 收口 |
| `cross_platform_inconsistency` | claude×{anthropic,kiro,bridge}；gemini×{native,antigravity} | claude-opus-4-*、gemini-2.5/3.x | 中 | 预期的 per-platform 路由真值；唯 kiro 估算 token 路径结构性有损 |

**关键非对称（系统性、刻意）**：唯一能一招退掉最大「未来风险」的代码改动**不是**给 chat 加守卫（那会反转操作员决策）——而是 media 路径已有的 fail-closed 守卫本身：它让任何**新上游渠道**的模型「到货即无价 → 被拒 → 人工定价是审批门」。chat 侧保持 serve-and-alert（分级成本、可用性优先、P0 兜底）是**设计**。

---

## 4. 不可服务台账（负面）—— 三类持久性

> **为什么要这张表**：servable-allowlist 只留「实测 200」，把**负面知识丢了**——于是每次 refresh 都重探已知打不通的模型（浪费 SSM），读者也看不到「X 为什么不在清单」。本台账把散落在代码注释/PR 里的实测负面证据固化，并按**持久性**分三类，关键是**别把临时失败记死成永久结论**。

**机器源当前摘要**：`ops/pricing/servable-reprobe-ledger.json` 维护 watchlist / skiplist / deadlist；分类口径以 JSON 字段为准，本文不再维护 derived 总数。

### 4.1 三类定义与处置

| 持久性 | 含义 | 处置 | 是否进探测 |
|---|---|---|---|
| **structural** | 上游弃用/下线、access-gated 404、走错平台/端点、非该 vendor 模型 | `skip_forever` | 永不重探（多数已在 deprecated-gate/alias-strip helper 自排除）|
| **transient** | 503 capacity / 瞬时 429/502 / 走错端点的无效探针 | `re_probe_periodically`（带日期）| **必须**周期性重探 |
| **policy** | 上游能服务但 TK 故意不上（无价、跨账号排除、无账号渠道）| `leave_excluded` | 永不探（探了也改不了状态）|

### 4.2 structural（永久跳过）—— 代表

- **anthropic**：`claude-3-*`/`3-5-*`/`3-7-*`/`4.0-*` 退役 dated 快照（friendly 400 建议新模型）；bracketed `[1m]/[Nk]` Claude-Code 上下文别名（硬 404 非模型，`tkStripContextWindowModelAlias` 预剥）；裸名 `opus`/`sonnet`/`haiku`（#617 400，`TkApplyBareModelAlias` 改写）；`claude-fable-5`（404 fable-mythos access-gated，2026-06-13，**带 caveat**：账号恢复 access 则下次 refresh 自动加回 → 严格说应进 reprobe，见 4.4）。
- **openai**：`codex-mini-latest`（稳定 400 "not supported with ChatGPT account"）；整族非目标 surface——embeddings/moderation/audio/realtime/tts/transcribe、o1/o3/o4、dall-e、computer-use、sora、legacy gpt-4*/3.5*、ct57 `-openai-compact` 别名。
- **gemini**：`gemini-2.0-*`/`gemini-3.x` chat（502，**project-scoped**：换 org-enabled Vertex project 会翻活 → 进 project-scoped skip-list，不进绝对 skip-list）。
- **antigravity**：`gemini-3-pro-high/low/preview`（200-but-0/0 静默退役）、`gemini-3.1-pro-high`（上游 deprecatedModelIds 400）——均有 live remap 别名、客户无感。
- **grok**：`grok-imagine-image-pro`（上游 2026-05-15 退役，已改 `grok-imagine-image-quality`）。
- **newapi**：`seedance-1.0-lite`、`Doubao-pro/lite-*k` legacy、`qwen2.5-coder-32b/7b`（退役）、embeddings/rerank 整族；`seedream-4.5/5.0`、`seedance-1.0-pro-fast`（上游有价但**不在 new-api 渠道常量**，桥接不可达）。

### 4.3 policy（能服务但故意不上）—— 代表

- **antigravity** claude-* 全族 + `gpt-oss-120b-medium`（操作员路由策略，`AntigravityConfigReconciler` 自愈）。
- **grok** `grok-4.x`/`grok-3`/`-search` 变体（unpriced-by-discipline，$0-P0；上价后才上）。
- **openai** `gpt-image-{1,1.5,2}`（真产品，缺 `api.model.images.request` scope；加 `type=apikey` 账号后可复测）。
- **newapi 聚合器/未接渠道**：~30 个无 TK 账号的 channel（Bedrock/OpenRouter/SiliconFlow/Mistral/Cohere/Perplexity/Midjourney/Kling/Jimeng/Vidu/Sora/Suno…）——见 §5。

### 4.4 transient（必须复测）—— reprobe watchlist（防错误记忆）

> **机器源**：`ops/pricing/servable-reprobe-ledger.json` 是 watchlist / skiplist / deadlist 的唯一可执行事实源；本节不再维护手写表。它按 `(platform, model)` 记录 `reason`、`last_probe` 或 `expires/freshness_days`、是否由 `refresh-servable-allowlist.py` 自动探测，以及不能自动探测时的 owner。
>
> **铁律**：watchlist 条目**不得**凭旧 transient 记录迁去永久 skip-list——只有**新探测结果**能移动它（200→升级进 allowlist；稳定的 structural 级失败→改判）。`refresh-servable-allowlist.py selftest` 已校验 watchlist freshness、watch/skip/dead 互斥、skip/dead 与 Go servable allowlist 互斥；preflight 会调用该 selftest。

### 4.5 如何固化（接线，不止是文档）

台账已接成**三个互斥列表**（而非一篇文档），且探针会**读** skip-list：

1. **永久 skip-list**：`ops/pricing/servable-reprobe-ledger.json.skiplist` 会从 `refresh-servable-allowlist.py candidates/probe/run` 的候选中排除条目；只放「死因与容量/端点无关」或当前 project-scoped 明确不可服务的项。
2. **reprobe watchlist**：`watchlist[].auto_probe=true` 的 native refresh 三元组（anthropic/openai/gemini）会自动并入探测候选；例如 `gpt-5.2/gpt-5.3-codex/codex-auto-review/gpt-5.x-codex` 与 gemini image wrong-surface 条目会按 `probe_family` 走正确 endpoint。`auto_probe=false` 的 antigravity/grok/newapi 长尾仍由 owner 轻量复测并更新同一 JSON。
3. **deadlist**：已退役或替代明确的 id 进入 `deadlist`，并由 apply gate 阻止新的 probe 结果把它们直接写回 allowlist。
4. **机械护栏**：`refresh-servable-allowlist.py selftest` 校验 watch/skip/dead 双成员、skip/dead 与 servable allowlist 双成员、watchlist freshness；`scripts/preflight.sh` 已调用该 selftest。

---

## 5. 扩展 backlog（channel_not_onboarded）

> 这些是 newapi 渠道适配器**理论可达、但当前无 TK 账号/价**——非缺陷，是有需求时走 `tokenkey-onboard-model` 的清单。补全本节即闭合「为什么 Mistral/Cohere/Kimi 不服务」的反复追问。

| 优先 | 渠道（ct）| 净增能力（native 没有的）| 备注 |
|---|---|---|---|
| 高 | ct=25 Moonshot | `kimi-k2.5` `kimi-k2-thinking` 等订阅 OAuth 长上下文 | 已有 billing fallback 价；典型 net-new |
| 中 | ct=35 MiniMax | `MiniMax-M2.x` chat + speech + `image-01` | 含音视频面 |
| 已完成 | ct=26 ZhipuV4 | `glm-4.5/4.6/4.7/5.x` 直连容量 | tk_044 + overlay + manifest + prod canary 已完成；`glm-4.7` livefire 200 且计费非零 |
| 中 | ct=17/43 Ali/DeepSeek 未接 id | `qwq-32b`；deepseek-v4 `-none/-max`（实为 adaptor 追加的思考后缀别名，非独立模型）| 2026-06-23 `qwq-32b` 与 `deepseek-v4-none/max` 均为 429 not_allowlisted；DeepSeek 正式 id `deepseek-v4-pro/flash` 与 `deepseek-chat/reasoner` 均 200；`qwen-turbo` 已由 tk_042 收敛 |
| 低 | ct=1/57 OpenAI 尾 | o1/o3/o4、gpt-4*/4o*、audio/embeddings/dall-e/sora-2（153+24）| 按 raw count 最大一桶 |
| 低 | ct=24/41 Gemini/Vertex 尾 | gemma、native-audio、robotics、computer-use 等 | 多为非目标 surface |
| 低 | ct=33 AWS Bedrock | claude + nova 全族 | 另一条 claude 路径 |
| 低 | ct=50/51/52/55 Kling/Jimeng/Vidu/Sora | 视频，**有账号即零代码 servable**（`IsVideoSupportedChannelType`）| |
| 低 | ct=2/5/36 Midjourney/Suno | 图/音乐生成 | |
| 低 | ct=20/49/53 OpenRouter/Coze/Submodel | 多 vendor 聚合（gpt-oss-120b、qwen3-coder-480b…）| **聚合器接入=channel 非平台**；会丢 5h/7d 窗/指纹/冷却归因，只接 native 没有的 net-new |
| — | ct=15/18/19/23/27/31/34/40/42/44/47/56 等 | Baidu/Xunfei/360/Tencent/Perplexity/Yi/Cohere/SiliconFlow/Mistral/MokaAI/Xinference/Replicate | 无账号，`leave_excluded` until 产品意图 |

> 聚合器纪律：CPA 等 OpenAI 兼容聚合器做成 newapi channel（零代码），做平台买不到东西；native 平台永不经它。

---

## 6. 维护与刷新

- **可服务 allowlist 刷新**：`/tokenkey-servable-model-refresh`（`ops/pricing/refresh-servable-allowlist.py`）——经 prod SSM 逐模型真实请求，只留 200，splice 回 5 个 Go map。探测元组当前为 anthropic/openai/gemini；antigravity/grok 手维护。
- **上架单个模型（served+priced）**：`/tokenkey-onboard-model`——probe → `tk_served_models.json` 清单 → `tk_NNN` model_mapping 迁移 + overlay fill-only 价（**官方源、禁臆造**）→ apply-live（scheduler_outbox 热更 + overlay sync-runtime）→ livefire 200 → 两档计费核对。
- **漂移门禁**：`scripts/checks/catalog-serving-drift.py`（manifest↔migration↔overlay 三方一致，priced-but-not-mapped 硬失败）经 `scripts/preflight.sh` 调用。
- **不可服务台账机器源**：`ops/pricing/servable-reprobe-ledger.json`（watchlist / skiplist / deadlist）由 `refresh-servable-allowlist.py selftest` 和 preflight 校验，避免 transient 记录过期或误进永久 skip。

---

## 附录 A. newapi 渠道目录（理论最大，56 channel types）

> 各 channel adaptor 的 `GetModelList()`（= new-api `channelId2Models`）。这是 newapi 第五平台的**理论可达上界**，不等于 TK 实际服务；实际服务由账号 `model_mapping` + 价格决定。每个 ct 的完整模型列表按文首「如何重生成渠道目录」的临时 test（`newapi.ChannelTypeModels()`）现抓。

| ct | 名称 | #模型 | TK 状态 |
|---|---|---|---|
| 1 | OpenAI | 153 | bridge 尾，未接 |
| 14 | Anthropic | 39 | claude bridge 面 |
| 16/26 | Zhipu/ZhipuV4 | 4/16 | ct26 GLM paid SKU 已由 tk_044 建模并完成 prod canary；ct16 legacy 不再作为目标路径 |
| 17 | Ali | 8 | 账号 60（部分）|
| 24 | Gemini | 45 | bridge 尾 |
| 25 | Moonshot | 5 | backlog（kimi）|
| 33 | AWS Bedrock | 25 | 未接（claude+nova）|
| 35 | MiniMax | 20 | backlog |
| 41 | VertexAI | 85 | gemini/claude bridge 面 |
| 43 | DeepSeek | 8 | 账号 39 |
| 45 | VolcEngine | 13 | 账号 7（doubao/seedream/seedance）|
| 48 | xAI | 22 | grok 原生 + ct48 聚合 |
| 54 | DoubaoVideo | — | seedance 视频 |
| 57 | ChatGPT 订阅(Codex) | 24 | codex bridge 面 |
| 2/4/7-13/15/18/19/20/22/23/27/31/34/36-40/42/44/46/47/49-53/55/56 | Midjourney/Ollama/各 GPT 代理/Baidu/Xunfei/360/OpenRouter/FastGPT/Tencent/Perplexity/Yi/Cohere/Suno/Dify/Jina/Cloudflare/SiliconFlow/Mistral/MokaAI/BaiduV2/Xinference/Coze/Kling/Jimeng/Vidu/Submodel/Sora/Replicate | — | 无 TK 账号（`leave_excluded`）|
