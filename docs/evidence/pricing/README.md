# Pricing evidence

Point-in-time vendor pricing captures live here. These files are provenance for
pricing overlays and audits; they are not the current pricing source of truth.

Current model-operations entry point: [`../../../ops/pricing/README.md`](../../../ops/pricing/README.md).
Runtime pricing data lives in code/data files such as
`backend/internal/service/tk_pricing_overlay.json` and the mirrored LiteLLM
fallback under `backend/resources/model-pricing/`.

Only captures that are referenced from overlay `source` / provenance strings are
kept here. Unreferenced snapshots are deleted (same discipline as #1992).

| File | Captured source |
| --- | --- |
| [`aliyun_pricing_20260612.md`](aliyun_pricing_20260612.md) | Alibaba DashScope pricing capture used by existing overlay provenance. |
| [`xai_pricing_20260815.md`](xai_pricing_20260815.md) | xAI Grok token pricing, inclusive 200k long-context tiers, and local usage-normalization evidence. |
