# OpenRouter Provider Onboarding (TokenKey)

Ops checklist for [OpenRouter provider application](https://openrouter.ai/providers/apply/form) and [provider integration docs](https://openrouter.ai/docs/guides/community/for-providers).

## Product posture

**Goal: multimodal seller** (text chat + image + video when supply is ready).  
`catalog_excluded_model_ids` is **not** a permanent “text-only” switch — it only hides models that are currently unservable (no healthy pool), unstable, or lacking a seller protocol (e.g. realtime audio has no OR audio surface yet).

**Source of truth for what we offer right now:** `GET /openrouter/v1/models` (and the billing-user alias `GET /v1/models`). Probes and the application form must follow that catalog: if a modality row is absent, treat media inference checks as N/A, not as a hard failure.

## API endpoints (paste into application form)

Always list catalog + chat. List image/video endpoints as **available protocol surfaces**; which models appear there is determined by the live catalog (rows with output modality `image` / `video`).

| Surface | URL | Notes for the form |
| --- | --- | --- |
| Models catalog | `https://api.tokenkey.dev/openrouter/v1/models` | Required; schema 2.4 |
| Alias models catalog | `https://api.tokenkey.dev/v1/models` | Same payload for billing-user seller keys |
| Chat inference | `https://api.tokenkey.dev/v1/chat/completions` | Required; public ids `tokenkey/<model>` |
| Image inference | `https://api.tokenkey.dev/openrouter/v1/images` | Protocol ready; models only if catalog lists `output` image |
| Video inference | `https://api.tokenkey.dev/openrouter/v1/videos` (+ poll `/openrouter/v1/videos/{id}`) | Protocol ready; models only if catalog lists `output` video |

Do **not** claim audio seller endpoints until an OR audio route exists. Audio-capable internal models stay in `catalog_excluded_model_ids` until then.

Catalog + inference auth: any API key owned by `billing_user_id` (ops bootstrap label: `openrouter`). No separate monitor/inference key split. Ops hygiene: `hygiene-keys` ensures one `openrouter` key and disables legacy `openrouter-inference` / `openrouter-monitor` names.

## Settings JSON

Write `tk_openrouter_provider_config` from `ops/pricing/examples/openrouter-provider-config.example.json`.

That example tracks **policy fields** (`billing_user_id`, exclude/stream lists, capacity, compliance URLs). It never stores raw API key strings or numeric key ids.

**Derived at runtime (do not duplicate in settings):**

- Supply groups ← `user_allowed_groups` for `billing_user_id`
- Seller auth ← any API key owned by `billing_user_id`

Change OR supply surface by editing user 32’s allowed groups only.

When a model becomes stably servable under those groups, **remove it from** `catalog_excluded_model_ids` (re-include). Keep excludes only for dead/unstable chat, missing supply, or modalities without a seller protocol.

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
```

Prod bootstrap / hygiene:

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
python3 ops/pricing/manage-openrouter-provider-config.py update-config  # upsert billing user + exclude/stream; unions example list SSOT
python3 ops/pricing/manage-openrouter-provider-config.py hygiene-keys   # ensure openrouter key; disable legacy dual names
```

Required fields in settings:

- `billing_user_id`: OR billing user (supply groups + key ownership)
- `catalog_excluded_model_ids`: internal model ids omitted from seller catalog
- `stream_only_model_ids`: chat models requiring `stream=true`

## P2 compliance fields

| Item | TokenKey value / action |
| --- | --- |
| Privacy policy | `https://tokenkey.dev/privacy` — must disclose prompt logging + retention + no-training-for-upstream |
| Terms of service | `https://tokenkey.dev/terms` |
| Status page | `https://status.tokenkey.dev` — **Better Stack Free**（已上线；CNAME → `statuspage.betteruptime.com`）。Caddy 自建 status vhost 已移除。运维说明：`ops/stage0/better-stack-status-page.md` |
| Monthly invoicing | set `invoicing_contact_email`; complete OR onboarding payout profile manually |

## Validation

Probes follow the **live catalog**: media inference is exercised only when
catalog rows exist for that modality; otherwise picks are N/A (not failures).
Serial smoke must pass for every currently listed catalog row before claiming
seller readiness for that surface.

```bash
python3 -m unittest ops.pricing.test_probe_openrouter_provider_chain
python3 ops/pricing/probe-openrouter-provider-chain.py --via-ssm --full-catalog
python3 ops/pricing/probe-openrouter-provider-inference-serial.py --via-ssm
TK_OR_PROVIDER_KEY=sk-or-seller python3 ops/pricing/export-openrouter-provider-models.py
go test -tags=unit ./backend/internal/service -run OpenRouter
go test -tags=unit ./backend/internal/handler -run OpenRouterProvider
```

Catalog exclude / stream-only lists live only in `tk_openrouter_provider_config` (template: `ops/pricing/examples/openrouter-provider-config.example.json`). Runtime reads settings on each catalog build; update JSON + `PUBLISH settings_updated` — no redeploy.

To patch prod exclude/stream lists:

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot  # read live config
# edit catalog_excluded_model_ids / stream_only_model_ids in settings JSON, then upsert via admin or update-config
```

Catalog uses OpenRouter provider **schema 2.4** (modality-owned pricing/capacity). Token-priced chat models expose text input/output modalities; media rows use output `image` (`completion` / `image`) or `video` (`completion` / `second`). Flat legacy catalog fields are not emitted.

## Inference model id contract

OpenRouter calls inference with the same `id` returned by `/openrouter/v1/models` (for example `tokenkey/deepseek-v4-pro`). TokenKey rewrites that public id back to the internal scheduling id before routing. Customer `/v1/*` gateway behavior is unchanged.

- Chat: `POST /v1/chat/completions`
- Image (output modality `type=image`): `POST /openrouter/v1/images` — OR schema in/out (`data[].b64_json`)
- Video (output modality `type=video`): `POST /openrouter/v1/videos` → `202` with `{id,polling_url,status}`; poll `GET /openrouter/v1/videos/{id}`
