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

Catalog auth: Bearer token using either `allowed_api_key_ids` (inference + catalog) or `monitor_api_key_ids` (catalog read-only).

## Settings JSON

Write `tk_openrouter_provider_config` from `ops/pricing/examples/openrouter-provider-config.example.json`.

That example tracks **prod IDs and URLs** (user 32, six supply `group_ids`, inference/monitor key ids). It never stores raw API key strings — only numeric ids. Re-sync after bootstrap or group/key changes:

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
# compare group_ids / allowed_api_key_ids / monitor_api_key_ids, then edit example if drifted
```

Prod bootstrap (creates group/keys/config; prints ids only, never key secrets):

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot
python3 ops/pricing/manage-openrouter-provider-config.py update-config  # upsert 6 group_ids + existing keys
```

Required fields:

- `group_ids`: OR supply groups (scheme C — no ct20/49/53 on public groups)
- `billing_user_id` + `allowed_api_key_ids`: OR production inference key
- `monitor_api_key_ids`: OR monitor key for `/v1/models` polling
- `catalog_excluded_model_ids`: internal model ids omitted from seller catalog (hot-updatable; omit key to use code defaults)
- `stream_only_model_ids`: chat models requiring `stream=true` (hot-updatable; omit key to use code defaults)

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

Catalog excludes unstable OR supply rows via `catalog_excluded_model_ids` in `tk_openrouter_provider_config` (defaults in code + example JSON). GLM stream-only models use `stream_only_model_ids`; emitted catalog rows get `openrouter.stream_required=true`. Update prod settings + `PUBLISH settings_updated` — no redeploy required.

To patch prod exclude/stream lists without touching group ids:

```bash
python3 ops/pricing/manage-openrouter-provider-config.py snapshot  # read live config
# edit catalog_excluded_model_ids / stream_only_model_ids in settings JSON, then upsert via admin or update-config
```

Catalog must include token-priced chat models plus media-priced rows (`pricing.image` for Imagen/Seedream, `pricing.request` for Veo) with matching `output_modalities`.

## Inference model id contract

OpenRouter calls inference with the same `id` returned by `/openrouter/v1/models` (for example `tokenkey/deepseek-v4-pro`). TokenKey rewrites that public id back to the internal scheduling id before routing.

- Chat: `POST /v1/chat/completions`
- Image (`output_modalities` includes `image`): `POST /openrouter/v1/images` — OR schema in/out (`data[].b64_json`)
- Video (`output_modalities` includes `video`): `POST /openrouter/v1/videos` → `202` with `{id,polling_url,status}`; poll `GET /openrouter/v1/videos/{id}`
