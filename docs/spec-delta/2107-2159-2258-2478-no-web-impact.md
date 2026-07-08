# spec-delta: upstream #2258 / #2107 / #2159 / #2478 no-web-impact

## Scope

- Upstream issues:
  - Wei-Shaw/sub2api#2258 (OpenAI 429 burst misclassified as window exhaustion → multi-day 503)
  - Wei-Shaw/sub2api#2107 (channel interval pricing skips flat default → \$0 billing for out-of-range requests)
  - Wei-Shaw/sub2api#2159 (disabled proxies still attached to accounts)
  - Wei-Shaw/sub2api#2478 (expired subscriptions block reassignment with 409 validity_days_mismatch)
- Backend changes:
  - `backend/internal/service/ratelimit_service.go`
  - `backend/internal/service/ratelimit_service_openai_test.go`
  - `backend/internal/service/rate_limit_429_cooldown_test.go`
  - `backend/internal/service/model_pricing_resolver.go`
  - `backend/internal/service/model_pricing_resolver_test.go`
  - `backend/internal/repository/account_repo.go`
  - `backend/internal/repository/account_repo_integration_test.go`
  - `backend/internal/repository/user_subscription_repo.go`
  - `backend/internal/repository/user_subscription_repo_integration_test.go`

## Intent

- #2258: when neither the 5h nor 7d Codex usage window is at 100%, return `nil` from `calculateOpenAI429ResetTime` so `handle429` falls through to the short configurable fallback cooldown (5s default) instead of applying the full multi-hour/day window-reset header.
- #2107: in `applyTokenOverrides`, always apply the channel's flat `InputPrice` / `OutputPrice` / `Cache*` / `ImageOutputPrice` fields to `BasePricing`. Out-of-range token counts now bill at the operator-configured channel default instead of falling back to LiteLLM catalog (or to `$0` for custom models with no catalog entry).
- #2159: filter `loadProxies` and the `WithProxy()` edge preload by `status='active'`. Disabled proxies no longer surface as `account.Proxy`, so outbound forwarding falls back to direct upstream — matching operator intent when toggling a proxy off.
- #2478: tighten `ExistsByUserIDAndGroupID` to mirror `GetActiveByUserIDAndGroupID` (status=active AND `expires_at > now()`). Expired and suspended subscriptions no longer block reassignment with 409 `validity_days_mismatch`.

## Web / API contract impact

- No frontend page, route, UI state, or i18n surface changed.
- No public API request/response schema changed.
- No admin settings schema changed.
- No new admin controls; no field added or removed in any admin view.
- No new error codes; the existing 409 `validity_days_mismatch` and 429 / 503 surfaces stay unchanged from the client's perspective.

## Why no web change is needed

- All four fixes change **internal** decision logic only:
  - #2258: log line + cooldown duration only; no client-visible response field added or removed. The new `openai_429_burst_below_window_limits` slog line is for operator observability.
  - #2107: the channel pricing config schema (intervals + flat fields) is unchanged. The fix only changes how those existing fields are *combined* at billing time. Existing operator UI flows still produce valid configs.
  - #2159: the proxy management admin UI is unchanged. `account.ProxyID` binding on the account record is preserved, so the admin UI still shows "bound to proxy X (disabled)"; only the runtime proxy attachment is suppressed.
  - #2478: the reassignment API contract is unchanged. The 409 still fires for active conflicts; this PR removes a false-positive trigger only.
- No new env var, deploy config, or migration is required.
