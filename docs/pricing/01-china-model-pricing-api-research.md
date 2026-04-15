# 国内 AI 模型基础定价 API 调研报告

> 调研日期：2026-04-13
> 调研范围：国内主流 AI 大模型平台的模型定价数据可编程获取能力

## 一、调研结论摘要

**核心发现：国内所有主流 AI 平台均不提供专用的模型定价 API 端点。**

定价信息仅通过以下渠道发布：

1. 官方文档页面（HTML，非结构化）
2. 控制台网页（需登录）
3. 第三方社区手动整理

这与 OpenRouter（提供 `/v1/models` 含定价字段）形成鲜明对比。

## 二、各平台详细调研

### 2.1 火山引擎 VolcEngine（方舟 Ark）


| 项目                              | 内容                                                                                             |
| ------------------------------- | ---------------------------------------------------------------------------------------------- |
| 渠道类型                            | NewAPI channel type 45                                                                         |
| 定价页面                            | [https://www.volcengine.com/docs/82379/1544106](https://www.volcengine.com/docs/82379/1544106) |
| 定价 API                          | **无**                                                                                          |
| `/v1/models` 或 `/api/v3/models` | 仅返回模型 ID，不含价格                                                                                  |
| 定价单位                            | 元/千 tokens                                                                                     |
| 定价特点                            | 按模型系列分层（Doubao-pro / lite / mini），同系列不同上下文窗口价格不同                                               |
| 特殊说明                            | 方舟是模型市场，同一 API Key 下可能包含多厂商模型（Doubao、Mistral、GLM 等），各模型定价不同                                    |


### 2.2 月之暗面 Moonshot（Kimi）


| 项目        | 内容                                                                                       |
| --------- | ---------------------------------------------------------------------------------------- |
| 渠道类型      | NewAPI channel type 34                                                                   |
| 定价页面      | [https://platform.kimi.ai/docs/pricing/chat](https://platform.kimi.ai/docs/pricing/chat) |
| 定价 API    | **无**                                                                                    |
| OpenAI 兼容 | `https://api.moonshot.ai/v1`，`/v1/models` 不含定价                                           |
| 定价单位      | $/1M tokens                                                                              |
| 定价特点      | 支持自动上下文缓存（Cache），缓存命中价格降低约 75%                                                           |
| 参考价格      | Kimi K2.5: $0.60 (Input) / $3.00 (Output)                                                |


### 2.3 DeepSeek（深度求索）


| 项目        | 内容                                                                                                     |
| --------- | ------------------------------------------------------------------------------------------------------ |
| 渠道类型      | NewAPI channel type 40                                                                                 |
| 定价页面      | [https://api-docs.deepseek.com/quick_start/pricing](https://api-docs.deepseek.com/quick_start/pricing) |
| 定价 API    | **无**                                                                                                  |
| OpenAI 兼容 | `https://api.deepseek.com/v1`，`/v1/models` 不含定价                                                        |
| 定价单位      | $/1M tokens                                                                                            |
| 定价特点      | 支持上下文缓存、闲时折扣（UTC 16:30-00:30）                                                                          |
| 参考价格      | deepseek-chat (V3.2): $0.14 (Input) / $0.28 (Output)；deepseek-reasoner: $0.55 (Input) / $2.19 (Output) |


### 2.4 MiniMax（稀宇科技）


| 项目     | 内容                                                                                                                         |
| ------ | -------------------------------------------------------------------------------------------------------------------------- |
| 渠道类型   | NewAPI channel type 43                                                                                                     |
| 定价页面   | [https://platform.minimax.io/docs/pricing/overview](https://platform.minimax.io/docs/pricing/overview)                     |
| 定价 API | **无**                                                                                                                      |
| 定价单位   | $/1M tokens                                                                                                                |
| 定价特点   | 支持 Token Plan（预购套餐）和 Pay-as-you-go                                                                                         |
| 历史价格   | 提供历史价格查询页：[https://platform.minimax.io/docs/faq/history-modelinfo](https://platform.minimax.io/docs/faq/history-modelinfo) |


### 2.5 智谱 AI（GLM）


| 项目     | 内容                                                                                                                                                                  |
| ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 渠道类型   | NewAPI channel type 31                                                                                                                                              |
| 定价页面   | [https://bigmodel.cn/pricing（国内）/](https://bigmodel.cn/pricing（国内）/) [https://docs.z.ai/guides/overview/pricing（国际）](https://docs.z.ai/guides/overview/pricing（国际）) |
| 定价 API | **无**（但 OpenAI 兼容 API 较完善）                                                                                                                                          |
| 定价单位   | 元/百万 tokens（国内）/ $/1M tokens（国际）                                                                                                                                    |
| 定价特点   | 部分模型免费（GLM-4.7-Flash、GLM-4.5-Flash）；Batch API 半价                                                                                                                    |
| 参考价格   | GLM-5: ¥50 (Input) / ¥200 (Output) per 1M tokens                                                                                                                    |


### 2.6 阿里云百炼（Qwen 通义千问）


| 项目     | 内容                                                                                                             |
| ------ | -------------------------------------------------------------------------------------------------------------- |
| 渠道类型   | NewAPI channel type 47                                                                                         |
| 定价页面   | [https://help.aliyun.com/zh/model-studio/model-pricing](https://help.aliyun.com/zh/model-studio/model-pricing) |
| 定价 API | **无**                                                                                                          |
| 定价单位   | 元/百万 tokens                                                                                                    |
| 定价特点   | 按区域定价（中国大陆 / 国际）；Batch 调用半价；支持上下文缓存折扣                                                                          |
| 特殊说明   | 百炼平台也是模型市场，除 Qwen 外还托管 DeepSeek、Llama 等第三方模型                                                                   |


### 2.7 百度千帆（文心 ERNIE）


| 项目     | 内容                                                       |
| ------ | -------------------------------------------------------- |
| 定价页面   | 千帆控制台内                                                   |
| 定价 API | **无**                                                    |
| 定价单位   | 元/万 tokens                                               |
| 定价特点   | 部分模型免费（ERNIE-3.5-8K、ERNIE-Speed-8K）                      |
| 参考价格   | ERNIE 4.5: ≈$0.55 (Input) / $2.20 (Output) per 1M tokens |


### 2.8 讯飞星火（Spark）


| 项目     | 内容                                                                                                     |
| ------ | ------------------------------------------------------------------------------------------------------ |
| 定价页面   | [https://global.xfyun.cn/doc/platform/pricing.html](https://global.xfyun.cn/doc/platform/pricing.html) |
| 定价 API | **无**                                                                                                  |
| 定价特点   | Spark Lite 永久免费；其他按 token 收费                                                                           |


## 三、第三方数据源调研

### 3.1 OpenRouter `/v1/models`


| 项目                | 内容                                                            |
| ----------------- | ------------------------------------------------------------- |
| 数据格式              | JSON，每个模型含 `pricing.prompt` / `pricing.completion`（USD/token） |
| 中国模型覆盖            | **有**，覆盖 Qwen、Kimi、DeepSeek、MiniMax、GLM 等（通过 OpenRouter 托管）   |
| VolcEngine/Doubao | **无**（OpenRouter 未对接 VolcEngine）                              |
| 更新频率              | 实时（模型上下线即更新）                                                  |
| 可用性               | 公开 API，无需认证                                                   |
| 局限性               | 价格为 OpenRouter 转售价，非官方直连价；不含中国独占平台（VolcEngine、百度千帆、讯飞）        |


### 3.2 models.dev `/api.json`


| 项目                | 内容                                                          |
| ----------------- | ----------------------------------------------------------- |
| 数据格式              | JSON，按 provider → model → cost (input/output per 1M tokens) |
| 中国模型覆盖            | **极少**，主要覆盖西方厂商                                             |
| VolcEngine/Doubao | **无**                                                       |
| 更新频率              | 社区维护                                                        |


### 3.3 LiteLLM `model_prices_and_context_window.json`


| 项目                | 内容                                                                           |
| ----------------- | ---------------------------------------------------------------------------- |
| 数据格式              | JSON，含 `input_cost_per_token` / `output_cost_per_token` / `litellm_provider` |
| 中国模型覆盖            | **不完整**。Qwen3 系列、MiniMax、GLM 被报告缺失定价字段（GitHub Issue #22646）                  |
| VolcEngine/Doubao | **仅 1 条**（`deepseek-v3-2-251201`，标记为 volcengine）                             |
| 更新频率              | 社区 PR 驱动                                                                     |
| 局限性               | 中国模型厂商自身参与度低，数据滞后                                                            |


### 3.4 new-api ratio sync


| 项目     | 内容                                                            |
| ------ | ------------------------------------------------------------- |
| 数据源    | 支持 4 种：另一个 new-api 实例、OpenRouter、models.dev、basellm.github.io |
| 中国模型覆盖 | 依赖数据源，basellm.github.io 预设中**无 Doubao**                       |
| 机制     | 管理员手动触发同步，非自动定时                                               |
| 优势     | 可从已配置好的 new-api 实例链式同步                                        |


### 3.5 Linux.do 社区


| 项目   | 内容                                                                   |
| ---- | -------------------------------------------------------------------- |
| 核心帖子 | [整理了部分大模型的官方API价格](https://linux.do/t/topic/1583944)（更新至 2026-03-02） |
| 数据格式 | Markdown 表格（非结构化）                                                    |
| 覆盖范围 | **最全**，覆盖百度、阿里、智谱、DeepSeek、VolcEngine、MiniMax、Moonshot 等             |
| 局限性  | 手动维护、非结构化、无 API、更新不及时                                                |
| 价值   | 可作为初始数据种子和交叉验证源                                                      |


### 3.6 AIGCRank


| 项目   | 内容                                                           |
| ---- | ------------------------------------------------------------ |
| 网址   | [https://aigcrank.cn/llmprice](https://aigcrank.cn/llmprice) |
| 数据格式 | HTML 表格（非结构化），含 USD/CNY 双币种                                  |
| 覆盖范围 | 国内外主流模型                                                      |
| API  | **无**                                                        |


### 3.7 pricepertoken.com


| 项目     | 内容                                                     |
| ------ | ------------------------------------------------------ |
| 网址     | [https://pricepertoken.com](https://pricepertoken.com) |
| 数据格式   | Web UI，支持 300+ 模型                                      |
| 中国模型覆盖 | 有（通过 OpenRouter 数据源）                                   |
| API    | **无**                                                  |
| 特点     | 提供历史价格趋势图                                              |


## 四、数据覆盖矩阵


| 厂商                  | 官方 API | OpenRouter | models.dev | LiteLLM | Linux.do |
| ------------------- | ------ | ---------- | ---------- | ------- | -------- |
| VolcEngine (Doubao) | ❌      | ❌          | ❌          | ⚠️ 1条   | ✅        |
| Moonshot (Kimi)     | ❌      | ✅          | ❌          | ⚠️ 部分   | ✅        |
| DeepSeek            | ❌      | ✅          | ✅          | ✅       | ✅        |
| MiniMax             | ❌      | ✅          | ❌          | ⚠️ 缺失   | ✅        |
| 智谱 (GLM)            | ❌      | ✅          | ❌          | ⚠️ 缺失   | ✅        |
| 阿里 (Qwen)           | ❌      | ✅          | ❌          | ⚠️ 缺失   | ✅        |
| 百度 (ERNIE)          | ❌      | ❌          | ❌          | ❌       | ✅        |
| 讯飞 (Spark)          | ❌      | ❌          | ❌          | ❌       | ✅        |


> ✅ = 完整覆盖 | ⚠️ = 部分/不完整 | ❌ = 无数据

## 五、关键洞察

1. **国内厂商统一不提供定价 API** — 这是行业共性，不是个别现象。定价信息仅在文档/控制台以人类可读格式发布。
2. **OpenRouter 是中国模型定价的最佳机器可读数据源** — 覆盖 Qwen、Kimi、DeepSeek、MiniMax、GLM，但不含 VolcEngine（Doubao）、百度、讯飞。价格为 OpenRouter 转售价，可能与官方直连价有差异。
3. **VolcEngine 是最大盲区** — 方舟平台的模型市场属性（托管多厂商模型）使其定价结构复杂，且无任何第三方数据源覆盖。
4. **LiteLLM 对中国模型的覆盖严重不足** — 作为 sub2api 当前的定价数据源，这是用户体验的核心痛点。
5. **Linux.do 社区是覆盖最全的中文数据源** — 但数据为非结构化 Markdown，无法直接程序化使用。
6. **定价变动频率较低** — 各平台通常每 3-6 个月调整一次定价，不需要高频抓取。

