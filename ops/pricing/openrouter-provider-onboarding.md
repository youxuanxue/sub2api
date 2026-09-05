# OpenRouter Provider Onboarding (TokenKey)

Ops checklist for [OpenRouter provider application](https://openrouter.ai/providers/apply/form) and [provider integration docs](https://openrouter.ai/docs/guides/community/for-providers).

## API endpoints (paste into application form)

| Surface | URL |
| --- | --- |
| Models catalog | `https://api.tokenkey.dev/openrouter/v1/models` |
| Alias models catalog | `https://api.tokenkey.dev/v1/models` (same payload for allowlisted OR/monitor keys) |
| Chat inference | `https://api.tokenkey.dev/v1/chat/completions` |
| Image inference | `https://api.tokenkey.dev/openrouter/v1/images` |
| Video inference | `https://api.tokenkey.dev/openrouter/v1/videos` (submit + poll `/openrouter/v1/videos/{id}`) |

Catalog auth: Bearer token on billing user keys named `openrouter-inference` (inference + catalog) or `openrouter-monitor` (catalog read-only).

## Settings JSON

Write `tk_openrouter_provider_config` from `ops/pricing/examples/openrouter-provider-config.example.json`.

That example tracks **policy fields** (`billing_user_id`, exclude/stream lists, capacity, compliance URLs). It never stores raw API key strings or numeric key ids.

**Derived at runtime (do not duplicate in settings):**

- Supply groups ← `user_allowed_groups` for `billing_user_id`
- Inference key ← billing user API key named `openrouter-inference`
- Monitor key ← billing user API key named `openrouter-monitor`

Change OR supply surface by editing user 32’s allowed groups / those two key names only.

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
```

Prod bootstrap (creates named keys + config; prints ids only, never key secrets):

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
python3 ops/pricing/manage-openrouter-provider-config.py update-config  # upsert billing user + exclude/stream; strips legacy group_ids / key id lists
```

Required fields in settings:

- `billing_user_id`: OR billing user (supply groups + key-name scope)
- `catalog_excluded_model_ids`: internal model ids omitted from seller catalog
- `stream_only_model_ids`: chat models requiring `stream=true`

## P2 compliance fields

| Item | TokenKey value / action |
| --- | --- |
| Privacy policy | `https://tokenkey.dev/privacy` — must disclose prompt logging + retention + no-training-for-upstream |
| Terms of service | `https://tokenkey.dev/terms` |
| Status page | set `status_page_url` (example: `https://status.tokenkey.dev`) before apply |
| Monthly invoicing | set `invoicing_contact_email`; complete OR onboarding payout profile manually |

## Validation

```bash
python3 ops/pricing/probe-openrouter-provider-chain.py --via-ssm --full-catalog
python3 ops/pricing/probe-openrouter-provider-inference-serial.py --via-ssm
TK_OR_PROVIDER_KEY=sk-or-monitor python3 ops/pricing/export-openrouter-provider-models.py
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
