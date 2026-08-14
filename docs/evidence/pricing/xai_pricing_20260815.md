# xAI Grok 官方定价与长上下文计费证据

> 抓取日期：2026-08-15。价格会变化；本文件只保存当日官方快照，供 TokenKey registry 对账与代码审查使用，不是运行时价格 owner。
> 运行时全局价格唯一 owner 仍是 `backend/internal/service/tk_pricing_overlay.json`。

## 官方来源

- 汇总定价页：<https://docs.x.ai/docs/pricing>
- `grok-4.6`：<https://docs.x.ai/developers/models/grok-4.6>
- `grok-4.5`：<https://docs.x.ai/developers/models/grok-4.5>
- `grok-4.3`：<https://docs.x.ai/developers/models/grok-4.3>
- `grok-build-0.1`：<https://docs.x.ai/developers/models/grok-build-0.1>
- `grok-4.20-0309-reasoning`：<https://docs.x.ai/developers/models/grok-4.20-0309-reasoning>
- `grok-4.20-0309-non-reasoning`：<https://docs.x.ai/developers/models/grok-4.20-0309-non-reasoning>
- `grok-4.20-multi-agent-0309`：<https://docs.x.ai/developers/models/grok-4.20-multi-agent-0309>
- Chat Completions usage schema：<https://docs.x.ai/api/endpoints#chat-completions>
- 退役模型迁移：<https://docs.x.ai/developers/migration/may-15-retirement>

## 长上下文语义

官方定价页原文：

> “Prices shown per million tokens. Models listed with two rows use long context pricing: requests whose prompt reaches the listed token threshold are billed at the higher rate for all tokens in the request.”

表格的高价行标为 `>= 200k prompt tokens`。因此本次证据支持以下完整策略：

- 阈值是 **inclusive**：正好 200,000 prompt tokens 即进入高价阶；
- 达到阈值后，整次请求的 input、cached input 与 output 都按高价行结算；
- 不是只对超过 200,000 的边际 token 加价。

## 官方价格快照

下表单位均为 USD / 1M tokens。

| Registry canonical owner | Prompt tier | Input | Cached input | Output |
| --- | --- | ---: | ---: | ---: |
| `grok-4.6` | `< 200k` | $2.00 | $0.50 | $6.00 |
| `grok-4.6` | `>= 200k` | $4.00 | $1.00 | $12.00 |
| `grok-4.5` | `< 200k` | $2.00 | $0.30 | $6.00 |
| `grok-4.5` | `>= 200k` | $4.00 | $0.60 | $12.00 |
| `grok-4.3` | `< 200k` | $1.25 | $0.20 | $2.50 |
| `grok-4.3` | `>= 200k` | $2.50 | $0.40 | $5.00 |
| `grok-build-0.1` | `< 200k` | $1.00 | $0.20 | $2.00 |
| `grok-build-0.1` | `>= 200k` | $2.00 | $0.40 | $4.00 |
| `grok-4.20-0309-reasoning` | `< 200k` | $1.25 | $0.20 | $2.50 |
| `grok-4.20-0309-reasoning` | `>= 200k` | $2.50 | $0.40 | $5.00 |
| `grok-4.20-0309-non-reasoning` | `< 200k` | $1.25 | $0.20 | $2.50 |
| `grok-4.20-0309-non-reasoning` | `>= 200k` | $2.50 | $0.40 | $5.00 |
| `grok-4.20-multi-agent-0309` | `< 200k` | $1.25 | $0.20 | $2.50 |
| `grok-4.20-multi-agent-0309` | `>= 200k` | $2.50 | $0.40 | $5.00 |

这些 model page 也逐项重复了相同阈值原文，并分别列出 500k、1M 或 256k context window；本次 registry 变更依据的是逐项价格表，而不是从 context window 外推价阶。

### 已证实的 direct alias

已有 registry direct row 会优先于 Go canonical owner resolver，因此它必须复制其语义 owner 的完整计费维度。本次只采用官方 model page 或退役迁移页已明确的 alias：

- `grok-4.3-latest` → `grok-4.3`；已有 `grok-latest` 也由既有官方 alias/probe 证据绑定到 `grok-4.3`；
- `grok-4.5-latest`、`grok-build-latest` → `grok-4.5`；
- `grok-code-fast`、`grok-code-fast-1`、`grok-code-fast-1-0825` → `grok-build-0.1`；
- 退役迁移页说明 reasoning 类退役 ID 由 `grok-4.3`（low reasoning effort）提供并按 `grok-4.3` 价格计费，因此已有兼容 row `grok-4-fast-reasoning` 复制 `grok-4.3` 的完整维度。

`grok-4.6-latest` 当前没有 direct registry row，由 Go routing/pricing owner resolver 读取 `grok-4.6`，不创建重复 owner。`grok-composer-2.5-fast` 没有公开独立价卡；本文件不为它制造官方价格声明。

## Prompt token 口径与本地 normalization

xAI Chat Completions usage schema 将 `prompt_tokens_details.text_tokens` 描述为总 text prompt tokens（包含 cached 与 non-cached text tokens），并将 `prompt_tokens_details.cached_tokens` 描述为此前请求复用的子集。官方文档没有把本地差分公式写成规范；TokenKey 必须由代码路径保证不重叠。

本次仓库审计证明 Grok Chat 与 Responses、流式与非流式都收敛到同一 usage parser：

- `backend/internal/service/openai_gateway_response_handling.go` 的 `extractOpenAIUsageFromJSONBytes` / `openAIUsageFromGJSON` 读取 envelope `input_tokens`（兼容回退 `prompt_tokens`）与 detail `cached_tokens` / `cache_write_tokens`；
- Grok fixture 明确覆盖 `input_tokens=5, cached_tokens=2` 与 `prompt_tokens=1, cached_tokens=1`，保留了 total/subset 关系；
- `backend/internal/service/openai_gateway_usage.go` 在结算前执行：

  ```text
  ordinary input = upstream total input - cache creation - cache read
  ```

  然后把 ordinary input、cache creation、cache read 放进三个互斥 `UsageTokens` bucket；
- long-context threshold 使用三个 bucket 的和，因此重构后仍等于 upstream total input；
- settlement 分别计算三个 bucket 的成本后相加，不重复计算。

因此当前实现满足 data publication 门禁：

```text
ordinary input + cache creation + cache read = upstream prompt/input token total
```

xAI 当日公开价卡没有独立 cache-write 费率，当前仓库里的 xAI fixture 也没有产生 cache-creation token。registry 因此不写 `cache_creation_input_token_cost`：对当前已观测的 xAI payload，cache-creation bucket 为零，cache read 使用官方 cached-input 价格。通用 parser 虽能承载未来的 `cache_write_tokens`，但当前通用 registry 结算在缺少 cache-creation 价时会保留零价；若 xAI 日后实际返回该字段，必须先取得 provider 费率证据或明确的本地 billing policy，再启用该路径，不能推断为普通 input 价。

## Registry 采用边界

- 本证据只覆盖上表七个 canonical owner 及“已证实的 direct alias”段列出的已有 direct rows。
- `grok-4.5` cached input 基础价应从旧 `$0.50/M` 纠正为官方 `$0.30/M`；高价阶通过 2x multiplier 得到 `$0.60/M`。
- 所有上述 tiered rows 必须同时携带：threshold `200000`、input multiplier `2`、output multiplier `2`、`long_context_threshold_inclusive: true`。
- 不把 provider/LiteLLM sensor、max context 或邻近型号当作定价事实。
- 不采纳 Batch API 折扣；本 registry 结算的是普通在线请求。
