# DashScope Text Embedding Pricing

Source: <https://help.aliyun.com/zh/model-studio/text-embedding-synchronous-api>,
Beijing table, captured 2026-09-09. Prices below are the synchronous input list
price in CNY per 1,000 tokens. Batch discounts and free quota are not list prices.

| Model | CNY / 1k input | Maximum input tokens | Default vector dimensions |
| --- | ---: | ---: | ---: |
| text-embedding-v1 | 0.0007 | 2048 | 1536 |
| text-embedding-v2 | 0.0007 | 2048 | 1536 |
| text-embedding-v3 | 0.0005 | 8192 | 1024 |
| text-embedding-v4 | 0.0005 | 8192 | 1024 |
| qwen3.7-text-embedding | 0.0005 | 128000 | 1024 |
| qwen3.7-text-embedding-flash | 0.000125 | 128000 | 1024 |

Registry conversion follows the existing CNY/USD basis of 6.7:
`input_cost_per_token = CNY_per_1k / 1000 / 6.7`. Output token price is zero.
The existing public list-tax policy remains responsible for displayed prices.

Both `ali/default` (supplier source 8) and `ali/default-2` (source 11) returned
HTTP 200 and nonempty vectors for every row using text input `hello` on
2026-09-09 11:01-11:03 UTC (19:01-19:03 Asia/Shanghai). Both DashScope native
text embedding and `/compatible-mode/v1/embeddings` were verified. SSM evidence:
`1e74bc70-38fe-4453-8f05-9871ec870f8e` and
`7646bcc2-49ad-476f-ac91-9caa1a10a535`. Credentials were not printed.

This change curates public pricing presentation. Existing BGE and
qwen3-embedding size-specific rows retain settlement pricing and mapping intent
with `display=false`. Multimodal embeddings are not added to public pricing.
These direct upstream results do not claim TokenKey gateway activation or live
usage attribution; production mappings and supplier sources are unchanged by
this PR.
