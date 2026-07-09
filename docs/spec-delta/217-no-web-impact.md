# spec-delta: PR #217 no-web-impact

## Scope
- PR: #217 (`fix/208-209-prod-ops`)
- Backend changes:
  - `backend/internal/service/gemini_oauth_service.go`
  - `backend/internal/handler/openai_gateway_handler.go`
- Deploy config changes:
  - `deploy/Caddyfile`
  - `deploy/aws/stage0/Caddyfile`
  - `deploy/aws/stage0/Caddyfile.edge`

## Intent
- Reduce prod-ops false-positive signals for #208/#209.
- Keep request handling behavior compatible while lowering expected-noise logs and streaming disconnect noise.

## Web / API contract impact
- No frontend page, route, UI state, or i18n surface changed.
- No request/response schema changes for public web/admin APIs.
- No OpenAPI/contract shape change required.

## Why no web change is needed
- #208 fix only changes internal log emission path/severity for expected Drive-scope fallback.
- #209 app-side fix only avoids writing extra SSE fallback payload after client context cancellation.
- #209 proxy-side fix only tunes Caddy reverse_proxy stream flush behavior.
