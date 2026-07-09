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
| [`aliyun_pricing_20260701.md`](aliyun_pricing_20260701.md) | Later Alibaba DashScope pricing capture for modelops work. |
| [`google_vertex_pricing_20260619.md`](google_vertex_pricing_20260619.md) | Google Vertex media pricing capture. |
| [`volcengine_pricing_20260611.md`](volcengine_pricing_20260611.md) | VolcEngine Ark pricing capture. |
