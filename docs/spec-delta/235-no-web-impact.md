# spec-delta: PR #235 no-web-impact

## Scope
- PR: #235 (`fix/compact-observability`)
- Backend changes:
  - `backend/internal/service/openai_gateway_messages.go`
  - `backend/internal/service/openai_gateway_service.go`
  - `backend/internal/service/openai_messages_compaction_tk.go`
  - `backend/internal/service/openai_compat_model_test.go`
- Sentinel registry changes:
  - `scripts/sentinels/gateway-tk.json`

## Intent
- Add production log evidence for OpenAI-compatible Messages compact-candidate requests.
- Capture output text length consistently across buffered and streaming Anthropic-compatible responses.
- Keep sentinel coverage for the observability hooks so future refactors do not silently remove compact incident evidence.

## Web / API contract impact
- No frontend page, route, UI state, or i18n surface changed.
- No public request/response schema changes.
- No admin setting, OpenAPI, or agent contract shape change required.

## Why no web change is needed
- The change only adds backend log fields and tests for gateway observability.
- Client-visible Anthropic-compatible response payloads are unchanged.
- Operators consume the new evidence from backend logs, not from Web UI or API contracts.
