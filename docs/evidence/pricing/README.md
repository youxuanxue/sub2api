# Pricing evidence

Point-in-time vendor pricing captures live here. These files are offline
provenance/import evidence only; they are not runtime pricing sources.

Current model-operations entry point: [`../../../ops/pricing/README.md`](../../../ops/pricing/README.md).
The only global runtime owner is
`backend/internal/service/tk_pricing_overlay.json`. A
`channel_model_pricing` row may override it only within its explicit
channel/group scope.

| File | Captured source |
| --- | --- |
| [`bigmodel_pricing_20260709.md`](bigmodel_pricing_20260709.md) | BigModel GLM pricing capture; pricing source only, GLM serving remains Alibaba DashScope via Qwen accounts. |
| [`aliyun_pricing_20260612.md`](aliyun_pricing_20260612.md) | Alibaba DashScope pricing capture used by existing overlay provenance. |
| [`aliyun_pricing_20260701.md`](aliyun_pricing_20260701.md) | Later Alibaba DashScope pricing capture for modelops work. |
| [`google_vertex_pricing_20260619.md`](google_vertex_pricing_20260619.md) | Google Vertex media pricing capture. |
| [`volcengine_pricing_20260611.md`](volcengine_pricing_20260611.md) | VolcEngine Ark pricing capture. |
