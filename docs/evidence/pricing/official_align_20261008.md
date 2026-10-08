# Official API pricing alignment (2026-10-08)

Source draft: GitHub Actions workflow `Pricing Registry Sensor`
(`pricing-registry-sensor.yml`, run [37770881144](https://github.com/youxuanxue/sub2api/actions/runs/37770881144)),
which opened draft PR #2493. Evidence feed: LiteLLM `model_prices_and_context_window.json`.

## Policy

- Apply LiteLLM **actionable** settlement fields only when they match **official USD** provider pages,
  or are additive priority-tier fields already published by the provider.
- **Defer** Dashscope / Moonshot / Kimi candidates: TokenKey CNY→USD basis is **÷6.7**
  (`docs/evidence/pricing/dashscope_text_embeddings_20260909.md`); LiteLLM USD must not overwrite that
  without a fresh CNY quote ÷6.7.

## Official checks

| Family | Official page | Verdict |
| --- | --- | --- |
| DeepSeek | https://api-docs.deepseek.com/quick_start/pricing | Peak USD for `deepseek-flash`/`deepseek-v4-pro` matches LiteLLM candidate (Flash peak in/cache/out = 0.3 / 0.006 / 1.2; Pro peak = 1.32 / 0.044 / 3.96). Registry previously looked like off-peak or CNY÷6.7; align to **official peak USD**. |
| Gemini | https://ai.google.dev/gemini-api/docs/pricing | Spot-checked Gemini 3.8 Flash paid standard $0.75/$3.75/$0.075 and priority $1.35/$6.75/$0.135 (through 2026-12-31); LiteLLM actionable fields for Gemini owners applied. |
| OpenAI | https://openai.com/api/pricing/ | Apply actionable fields (mostly priority-tier + TTS input). |
| Anthropic | https://www.anthropic.com/pricing | Only `claude-sonnet-5-5` cache_read actionable in this sensor batch. |
| xAI / Grok | https://docs.x.ai/docs/models | Apply LiteLLM actionable Grok owner fields. |
| Qwen / Moonshot / Kimi | Aliyun / Moonshot CNY pages | **Deferred** — keep CNY÷6.7 until re-quoted. |

## Applied owners (65)

| owner | family | field | before ($/MTok) | after ($/MTok) |
| --- | --- | --- | ---: | ---: |
| `claude-sonnet-5-5` | claude | `cache_read_input_token_cost` | 0.2 | 0.1 |
| `deepseek-v3.2` | deepseek | `cache_read_input_token_cost` | null | 0.028 |
| `deepseek-v3.2` | deepseek | `input_cost_per_token` | 0.298507 | 0.28 |
| `deepseek-v3.2` | deepseek | `output_cost_per_token` | 0.447761 | 0.4 |
| `deepseek-v4-flash` | deepseek | `cache_read_input_token_cost` | 0.00298507 | 0.006 |
| `deepseek-v4-flash` | deepseek | `input_cost_per_token` | 0.149254 | 0.3 |
| `deepseek-v4-flash` | deepseek | `output_cost_per_token` | 0.597015 | 1.2 |
| `deepseek-v4-pro` | deepseek | `cache_read_input_token_cost` | 0.0223881 | 0.044 |
| `deepseek-v4-pro` | deepseek | `input_cost_per_token` | 0.671642 | 1.32 |
| `deepseek-v4-pro` | deepseek | `output_cost_per_token` | 2.01493 | 3.96 |
| `gemini-2.5-flash` | gemini | `cache_read_input_token_cost_priority` | null | 0.054 |
| `gemini-2.5-flash` | gemini | `input_cost_per_token_priority` | null | 0.54 |
| `gemini-2.5-flash` | gemini | `output_cost_per_token_priority` | null | 4.5 |
| `gemini-2.5-flash-lite` | gemini | `cache_read_input_token_cost_priority` | null | 0.018 |
| `gemini-2.5-flash-lite` | gemini | `input_cost_per_token_priority` | null | 0.18 |
| `gemini-2.5-flash-lite` | gemini | `output_cost_per_token_priority` | null | 0.72 |
| `gemini-2.5-flash-native-audio-latest` | gemini | `input_cost_per_token` | 0.3 | 0.5 |
| `gemini-2.5-flash-native-audio-latest` | gemini | `output_cost_per_token` | 2.5 | 2 |
| `gemini-2.5-flash-native-audio-preview-09-2025` | gemini | `input_cost_per_token` | 0.3 | 0.5 |
| `gemini-2.5-flash-native-audio-preview-09-2025` | gemini | `output_cost_per_token` | 2.5 | 2 |
| `gemini-2.5-flash-native-audio-preview-12-2025` | gemini | `input_cost_per_token` | 0.3 | 0.5 |
| `gemini-2.5-flash-native-audio-preview-12-2025` | gemini | `output_cost_per_token` | 2.5 | 2 |
| `gemini-2.5-flash-preview-09-2025` | gemini | `cache_read_input_token_cost` | 0.075 | 0.03 |
| `gemini-2.5-flash-preview-tts` | gemini | `input_cost_per_token` | 0.3 | 0.5 |
| `gemini-2.5-flash-preview-tts` | gemini | `output_cost_per_token` | 2.5 | 10 |
| `gemini-2.5-pro` | gemini | `cache_read_input_token_cost_priority` | null | 0.225 |
| `gemini-2.5-pro` | gemini | `input_cost_per_token_priority` | null | 2.25 |
| `gemini-2.5-pro` | gemini | `output_cost_per_token_priority` | null | 18 |
| `gemini-2.5-pro-preview-tts` | gemini | `input_cost_per_token` | 1.25 | 1 |
| `gemini-2.5-pro-preview-tts` | gemini | `output_cost_per_token` | 10 | 20 |
| `gemini-3-pro-image` | gemini | `cache_read_input_token_cost` | null | 0.2 |
| `gemini-3-pro-image` | gemini | `cache_read_input_token_cost_priority` | null | 0.36 |
| `gemini-3-pro-image` | gemini | `input_cost_per_token_priority` | null | 3.6 |
| `gemini-3-pro-image` | gemini | `output_cost_per_token_priority` | null | 21.6 |
| `gemini-3-pro-image-preview` | gemini | `cache_read_input_token_cost` | null | 0.2 |
| `gemini-3-pro-image-preview` | gemini | `cache_read_input_token_cost_priority` | null | 0.36 |
| `gemini-3-pro-image-preview` | gemini | `input_cost_per_token_priority` | null | 3.6 |
| `gemini-3-pro-image-preview` | gemini | `output_cost_per_token_priority` | null | 21.6 |
| `gemini-3.1-flash-image` | gemini | `cache_read_input_token_cost` | null | 0.05 |
| `gemini-3.1-flash-image` | gemini | `cache_read_input_token_cost_priority` | null | 0.09 |
| `gemini-3.1-flash-image` | gemini | `input_cost_per_token_priority` | null | 0.9 |
| `gemini-3.1-flash-image` | gemini | `output_cost_per_token_priority` | null | 5.4 |
| `gemini-3.1-flash-image-preview` | gemini | `cache_read_input_token_cost` | null | 0.05 |
| `gemini-3.6-flash` | gemini | `cache_read_input_token_cost` | 0.15 | 0.075 |
| `gemini-3.6-flash` | gemini | `cache_read_input_token_cost_priority` | 0.27 | 0.135 |
| `gemini-3.6-flash` | gemini | `input_cost_per_token` | 1.5 | 0.75 |
| `gemini-3.6-flash` | gemini | `input_cost_per_token_priority` | 2.7 | 1.35 |
| `gemini-3.6-flash` | gemini | `output_cost_per_token` | 7.5 | 3.75 |
| `gemini-3.6-flash` | gemini | `output_cost_per_token_priority` | 13.5 | 6.75 |
| `gemini-3.7-flash` | gemini | `cache_read_input_token_cost_priority` | null | 0.135 |
| `gemini-3.7-flash` | gemini | `input_cost_per_token_priority` | null | 1.35 |
| `gemini-3.7-flash` | gemini | `output_cost_per_token_priority` | null | 6.75 |
| `gemini-3.8-flash` | gemini | `cache_read_input_token_cost_priority` | null | 0.135 |
| `gemini-3.8-flash` | gemini | `input_cost_per_token_priority` | null | 1.35 |
| `gemini-3.8-flash` | gemini | `output_cost_per_token_priority` | null | 6.75 |
| `gemini-embedding-2` | gemini | `input_cost_per_image_token` | null | 0.45 |
| `gemini-embedding-2-preview` | gemini | `input_cost_per_image_token` | null | 0.45 |
| `gemini-flash-latest` | gemini | `cache_read_input_token_cost` | 0.03 | 0.075 |
| `gemini-flash-latest` | gemini | `cache_read_input_token_cost_priority` | null | 0.135 |
| `gemini-flash-latest` | gemini | `input_cost_per_token` | 0.3 | 0.75 |
| `gemini-flash-latest` | gemini | `input_cost_per_token_priority` | null | 1.35 |
| `gemini-flash-latest` | gemini | `output_cost_per_token` | 2.5 | 3.75 |
| `gemini-flash-latest` | gemini | `output_cost_per_token_priority` | null | 6.75 |
| `gemini-flash-lite-latest` | gemini | `cache_read_input_token_cost` | 0.01 | 0.03 |
| `gemini-flash-lite-latest` | gemini | `cache_read_input_token_cost_priority` | null | 0.05 |
| `gemini-flash-lite-latest` | gemini | `input_cost_per_token` | 0.1 | 0.3 |
| `gemini-flash-lite-latest` | gemini | `input_cost_per_token_priority` | null | 0.54 |
| `gemini-flash-lite-latest` | gemini | `output_cost_per_token` | 0.4 | 2.5 |
| `gemini-flash-lite-latest` | gemini | `output_cost_per_token_priority` | null | 4.5 |
| `gemini-live-2.5-flash-preview-native-audio-09-2025` | gemini | `input_cost_per_image_token` | null | 3 |
| `gemini-live-2.5-flash-preview-native-audio-09-2025` | gemini | `input_cost_per_token` | 0.3 | 0.5 |
| `gemini-pro-latest` | gemini | `cache_read_input_token_cost` | 0.125 | 0.2 |
| `gemini-pro-latest` | gemini | `cache_read_input_token_cost_priority` | null | 0.36 |
| `gemini-pro-latest` | gemini | `input_cost_per_token` | 1.25 | 2 |
| `gemini-pro-latest` | gemini | `input_cost_per_token_priority` | null | 3.6 |
| `gemini-pro-latest` | gemini | `output_cost_per_token` | 10 | 12 |
| `gemini-pro-latest` | gemini | `output_cost_per_token_priority` | null | 21.6 |
| `gpt-4.1-2025-04-14` | openai | `cache_read_input_token_cost_priority` | null | 0.875 |
| `gpt-4.1-2025-04-14` | openai | `input_cost_per_token_priority` | null | 3.5 |
| `gpt-4.1-2025-04-14` | openai | `output_cost_per_token_priority` | null | 14 |
| `gpt-4.1-mini-2025-04-14` | openai | `cache_read_input_token_cost_priority` | null | 0.175 |
| `gpt-4.1-mini-2025-04-14` | openai | `input_cost_per_token_priority` | null | 0.7 |
| `gpt-4.1-mini-2025-04-14` | openai | `output_cost_per_token_priority` | null | 2.8 |
| `gpt-4.1-nano-2025-04-14` | openai | `cache_read_input_token_cost_priority` | null | 0.05 |
| `gpt-4.1-nano-2025-04-14` | openai | `input_cost_per_token_priority` | null | 0.2 |
| `gpt-4.1-nano-2025-04-14` | openai | `output_cost_per_token_priority` | null | 0.8 |
| `gpt-4o-2024-08-06` | openai | `cache_read_input_token_cost_priority` | null | 2.125 |
| `gpt-4o-2024-08-06` | openai | `input_cost_per_token_priority` | null | 4.25 |
| `gpt-4o-2024-08-06` | openai | `output_cost_per_token_priority` | null | 17 |
| `gpt-4o-2024-11-20` | openai | `cache_read_input_token_cost_priority` | null | 2.125 |
| `gpt-4o-2024-11-20` | openai | `input_cost_per_token_priority` | null | 4.25 |
| `gpt-4o-2024-11-20` | openai | `output_cost_per_token_priority` | null | 17 |
| `gpt-4o-mini-2024-07-18` | openai | `cache_read_input_token_cost_priority` | null | 0.125 |
| `gpt-4o-mini-2024-07-18` | openai | `input_cost_per_token_priority` | null | 0.25 |
| `gpt-4o-mini-2024-07-18` | openai | `output_cost_per_token_priority` | null | 1 |
| `gpt-4o-mini-tts` | openai | `input_cost_per_token` | 2.5 | 0.6 |
| `gpt-4o-mini-tts-2025-03-20` | openai | `input_cost_per_token` | 2.5 | 0.6 |
| `gpt-4o-mini-tts-2025-12-15` | openai | `input_cost_per_token` | 2.5 | 0.6 |
| `gpt-5-nano-2025-08-07` | openai | `input_cost_per_token_priority` | null | 2.5 |
| `gpt-5.5` | openai | `cache_read_input_token_cost_priority` | 1 | 1.25 |
| `gpt-5.5` | openai | `input_cost_per_token_priority` | 10 | 12.5 |
| `gpt-5.5` | openai | `output_cost_per_token_priority` | 60 | 75 |
| `gpt-5.5-2026-04-23` | openai | `cache_read_input_token_cost_priority` | 1 | 1.25 |
| `gpt-5.5-2026-04-23` | openai | `input_cost_per_token_priority` | 10 | 12.5 |
| `gpt-5.5-2026-04-23` | openai | `output_cost_per_token_priority` | 60 | 75 |
| `gpt-5.6-sol` | openai | `cache_creation_input_token_cost` | 6.25 | 5 |
| `gpt-5.6-sol` | openai | `cache_creation_input_token_cost_priority` | 12.5 | 10 |
| `gpt-5.6-sol` | openai | `cache_read_input_token_cost` | 0.5 | 0.4 |
| `gpt-5.6-sol` | openai | `cache_read_input_token_cost_above_272k_tokens` | 1 | 0.8 |
| `gpt-5.6-sol` | openai | `cache_read_input_token_cost_priority` | 1 | 0.8 |
| `gpt-5.6-sol` | openai | `input_cost_per_token` | 5 | 4 |
| `gpt-5.6-sol` | openai | `input_cost_per_token_above_272k_tokens` | 10 | 8 |
| `gpt-5.6-sol` | openai | `input_cost_per_token_priority` | 10 | 8 |
| `gpt-5.6-sol` | openai | `output_cost_per_token` | 30 | 20 |
| `gpt-5.6-sol` | openai | `output_cost_per_token_above_272k_tokens` | 45 | 30 |
| `gpt-5.6-sol` | openai | `output_cost_per_token_priority` | 60 | 40 |
| `gpt-6-sol` | openai | `output_cost_per_token_above_272k_tokens` | 15 | 15 |
| `gpt-6.1-sol` | openai | `output_cost_per_token_above_272k_tokens` | 15 | 15 |
| `gpt-realtime` | openai | `input_cost_per_image_token` | null | 5 |
| `gpt-realtime-1.5` | openai | `input_cost_per_image_token` | null | 5 |
| `gpt-realtime-2` | openai | `input_cost_per_image_token` | null | 5 |
| `gpt-realtime-2` | openai | `output_cost_per_token` | 16 | 24 |
| `gpt-realtime-2.1` | openai | `input_cost_per_image_token` | null | 5 |
| `gpt-realtime-2.1-mini` | openai | `input_cost_per_image_token` | null | 0.8 |
| `gpt-realtime-2025-08-28` | openai | `input_cost_per_image_token` | null | 5 |
| `gpt-realtime-mini` | openai | `cache_read_input_token_cost` | null | 0.06 |
| `gpt-realtime-mini` | openai | `input_cost_per_image_token` | null | 0.8 |
| `gpt-realtime-mini-2025-12-15` | openai | `input_cost_per_image_token` | null | 0.8 |
| `grok-4.20-0309-non-reasoning` | grok | `input_cost_per_image_token` | null | 1.25 |
| `grok-4.20-0309-reasoning` | grok | `input_cost_per_image_token` | null | 1.25 |
| `grok-4.20-multi-agent-0309` | grok | `input_cost_per_image_token` | null | 1.25 |
| `grok-4.3` | grok | `input_cost_per_image_token` | null | 1.25 |
| `grok-4.3-latest` | grok | `input_cost_per_image_token` | null | 1.25 |
| `grok-4.5` | grok | `input_cost_per_image_token` | null | 2 |
| `grok-4.5-latest` | grok | `input_cost_per_image_token` | null | 2 |
| `grok-4.6` | grok | `input_cost_per_image_token` | null | 2 |
| `grok-4.7` | grok | `input_cost_per_image_token` | null | 2 |
| `grok-build-0.1` | grok | `input_cost_per_image_token` | null | 1 |
| `grok-build-latest` | grok | `input_cost_per_image_token` | null | 2 |
| `grok-code-fast` | grok | `input_cost_per_image_token` | null | 1 |
| `grok-code-fast-1` | grok | `input_cost_per_image_token` | null | 1 |
| `grok-code-fast-1-0825` | grok | `input_cost_per_image_token` | null | 1 |
| `o3-2025-04-16` | openai | `cache_read_input_token_cost_priority` | null | 0.875 |
| `o3-2025-04-16` | openai | `input_cost_per_token_priority` | null | 3.5 |
| `o3-2025-04-16` | openai | `output_cost_per_token_priority` | null | 14 |
| `o4-mini-2025-04-16` | openai | `cache_read_input_token_cost_priority` | null | 0.5 |
| `o4-mini-2025-04-16` | openai | `input_cost_per_token_priority` | null | 2 |
| `o4-mini-2025-04-16` | openai | `output_cost_per_token_priority` | null | 8 |

## Deferred owners (17)

- `kimi-k2.5` (cny_fx): `output_cost_per_token`
- `kimi-k2.6` (cny_fx): `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`
- `kimi-k2.7-code` (cny_fx): `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`
- `kimi-k3` (cny_fx): `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-128k` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-128k-vision-preview` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-32k` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-32k-vision-preview` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-8k` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-8k-vision-preview` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `moonshot-v1-auto` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `qwen-max` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `qwen-plus` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `qwen-turbo` (cny_fx): `input_cost_per_token`, `output_cost_per_token`
- `qwen3.7-max` (cny_fx): `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`
- `qwen3.8-flash` (cny_fx): `cache_creation_input_token_cost`, `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`
- `qwen3.8-max` (cny_fx): `cache_read_input_token_cost`, `input_cost_per_token`, `output_cost_per_token`

