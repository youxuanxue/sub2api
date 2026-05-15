# spec-delta: PR #222 no-web-impact

## Scope
- PR: #222 (`fix/429-no-reset-cooldown`)
- Backend changes:
  - `backend/internal/service/ratelimit_service.go`
  - `backend/internal/service/rate_limit_429_cooldown_test.go`
- Sentinel registry changes:
  - `scripts/gateway-tk-sentinels.json`

## Intent
- Apply the existing configurable short 429 cooldown when Anthropic returns a 429 without reset metadata.
- Preserve passthrough for known Anthropic third-party / extra-usage rejections so those non-quota errors do not locally rate-limit the account.

## Web / API contract impact
- No frontend page, route, UI state, or i18n surface changed.
- No request/response schema changes for public web/admin APIs.
- No settings schema or OpenAPI/contract shape change required; this reuses the existing `rate_limit_429_cooldown` backend setting.

## Why no web change is needed
- The change only affects backend account scheduling after an upstream Anthropic 429 response that lacks reset metadata.
- Operators already control the fallback cooldown through the existing backend setting.
- Client-visible API payloads and admin UI configuration surfaces are unchanged.
