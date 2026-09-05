# Pricing evidence

Point-in-time vendor pricing captures live here. These files are provenance for
pricing overlays and audits; they are not the current pricing source of truth.

Current model-operations entry point: [`../../../ops/pricing/README.md`](../../../ops/pricing/README.md).
Runtime pricing data lives in code/data files such as
`backend/internal/service/tk_pricing_overlay.json` and the mirrored LiteLLM
fallback under `backend/resources/model-pricing/`.

| File | Captured source |
| --- | --- |
| [`bigmodel_pricing_20260709.md`](bigmodel_pricing_20260709.md) | BigModel GLM pricing capture; pricing source only, GLM serving remains Alibaba DashScope via Qwen accounts. |
| [`aliyun_pricing_20260612.md`](aliyun_pricing_20260612.md) | Alibaba DashScope pricing capture used by existing overlay provenance. |
| [`google_vertex_pricing_20260619.md`](google_vertex_pricing_20260619.md) | Google Vertex media pricing capture. |
| [`xai_pricing_20260815.md`](xai_pricing_20260815.md) | xAI Grok token pricing, inclusive 200k long-context tiers, and local usage-normalization evidence. |
| [`volcengine_pricing_20260611.md`](volcengine_pricing_20260611.md) | VolcEngine Ark pricing capture. |
