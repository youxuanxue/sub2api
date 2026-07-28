# OpenRouter Provider Onboarding (TokenKey)

Ops checklist for [OpenRouter provider application](https://openrouter.ai/providers/apply/form) and [provider integration docs](https://openrouter.ai/docs/guides/community/for-providers).

## API endpoints (paste into application form)

| Surface | URL |
| --- | --- |
| Models catalog | `https://api.tokenkey.dev/openrouter/v1/models` |
| Alias models catalog | `https://api.tokenkey.dev/v1/models` (same payload for allowlisted OR/monitor keys) |
| Inference | `https://api.tokenkey.dev/v1/chat/completions` |

Catalog auth: Bearer token using either `allowed_api_key_ids` (inference + catalog) or `monitor_api_key_ids` (catalog read-only).

## Settings JSON

Write `tk_openrouter_provider_config` from `ops/pricing/examples/openrouter-provider-config.example.json`.

Required fields:

- `group_ids`: OR supply groups (scheme C — no ct20/49/53 on public groups)
- `billing_user_id` + `allowed_api_key_ids`: OR production inference key
- `monitor_api_key_ids`: OR monitor key for `/v1/models` polling

## P2 compliance fields

| Item | TokenKey value / action |
| --- | --- |
| Privacy policy | `https://tokenkey.dev/privacy` — must disclose prompt logging + retention + no-training-for-upstream |
| Terms of service | `https://tokenkey.dev/terms` |
| Status page | set `status_page_url` (example: `https://status.tokenkey.dev`) before apply |
| Monthly invoicing | set `invoicing_contact_email`; complete OR onboarding payout profile manually |

## Validation

```bash
TK_OR_PROVIDER_KEY=sk-or-monitor python3 ops/pricing/export-openrouter-provider-models.py
go test -tags=unit ./backend/internal/service -run OpenRouter
go test -tags=unit ./backend/internal/handler -run OpenRouterProvider
```

## Inference model id contract

OpenRouter calls `POST /v1/chat/completions` with the same `id` returned by `/openrouter/v1/models` (for example `tokenkey/deepseek-v4-pro`). TokenKey rewrites that public id back to the internal scheduling id before routing.
