package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAI OAuth "Rate limit exceeded" 429s (account pressure, not the >=100%
// usage-exhausted window) previously left only a Redis ranking penalty and a
// 5s in-memory block that the runtime-block check drops when no persisted
// cooldown exists. The same throttled credential was re-picked on the next
// request. This owner persists a short account+model cooldown through the
// same model_rate_limits store as openai_capacity_retry_after_tk.go so every
// instance's candidate filter (IsSchedulableForModelWithContext) skips it.
const (
	// Default when the 429 carries no Retry-After.
	// Long enough to span several request waves at hundreds of req/min, short
	// enough that a fully cooled pool recovers within one client retry.
	openAIOAuth429PressureModelCooldownDefault = 30 * time.Second
	// Floor matches openAIOAuth429FallbackCooldown: a sub-second Retry-After
	// would otherwise expire before the next request reaches selection.
	openAIOAuth429PressureModelCooldownFloor = openAIOAuth429FallbackCooldown
	// Cap reuses the capacity Retry-After cap. Sub-100% x-codex reset-after
	// values describe a whole 5h/7d window; the long cooldown for those stays
	// on the >=100% path (upstream #2258 policy).
	openAIOAuth429PressureModelCooldownCap = openAICapacityRetryAfterModelCooldownCap

	openAIOAuth429PressureModelCooldownReason = "openai_oauth_429_rate_limit_pressure"
)

// openAIModelCooldownReasonMatchesWireAliases reports reasons whose model
// cooldown is stored on the scheduling model and must also block its Codex
// wire alias.
func openAIModelCooldownReasonMatchesWireAliases(reason string) bool {
	return reason == openAICapacityRetryAfterModelCooldownReason ||
		reason == openAIOAuth429PressureModelCooldownReason
}

// openAIOAuth429PressureCooldownDuration picks Retry-After (header or error
// envelope), else the default, then clamps to [floor, cap]. x-codex
// reset-after is not used: it is the 5h/7d window reset, not the burst
// throttle, and would pin almost every pressure 429 to the cap.
func openAIOAuth429PressureCooldownDuration(headers http.Header, body []byte, now time.Time) time.Duration {
	delay := openAIOAuth429PressureModelCooldownDefault
	if retryAfter, ok := openAICapacityRetryAfterDuration(headers, body, now); ok && retryAfter > 0 {
		delay = retryAfter
	}
	if delay < openAIOAuth429PressureModelCooldownFloor {
		delay = openAIOAuth429PressureModelCooldownFloor
	}
	if delay > openAIOAuth429PressureModelCooldownCap {
		delay = openAIOAuth429PressureModelCooldownCap
	}
	return delay
}

// tkApplyOpenAIOAuth429PressureModelCooldown persists a short account+model
// cooldown for an OpenAI OAuth account-pressure 429. It never touches account
// status, RateLimitResetAt, overload or temp-unschedulable fields, and it does
// not replace a model cooldown that already lasts at least as long.
func (s *OpenAIGatewayService) tkApplyOpenAIOAuth429PressureModelCooldown(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	body []byte,
	canonicalModel ...string,
) bool {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.accountRepo == nil || account == nil {
		return false
	}
	if statusCode != http.StatusTooManyRequests || !isOpenAIAccountRateLimitPressure(account, headers, body) {
		return false
	}
	model := strings.TrimSpace(firstNonEmpty(canonicalModel...))
	if model == "" {
		return false
	}
	now := time.Now()
	delay := openAIOAuth429PressureCooldownDuration(headers, body, now)
	resetAt := now.Add(delay)
	if existing := account.modelRateLimitResetAt(model); existing != nil && !existing.Before(resetAt) {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if err := s.rateLimitService.accountRepo.SetModelRateLimit(stateCtx, account.ID, model, resetAt, openAIOAuth429PressureModelCooldownReason); err != nil {
		slog.Warn("openai_oauth_429_pressure_model_cooldown_failed",
			"account_id", account.ID,
			"model", model,
			"error", err)
		return false
	}
	setAccountModelRateLimitSnapshot(account, model, resetAt, openAIOAuth429PressureModelCooldownReason, now)
	slog.Info("openai_oauth_429_pressure_model_cooldown",
		"account_id", account.ID,
		"model", model,
		"reset_at", resetAt,
		"cooldown_seconds", int(delay.Seconds()))
	return true
}

// openAIOAuth429StormActive reports whether this instance saw at least
// openAIOAuth429StormThreshold OAuth 429s in the current storm window.
func (s *OpenAIGatewayService) openAIOAuth429StormActive(now time.Time) bool {
	if s == nil {
		return false
	}
	windowStart := s.openaiOAuth429WindowStartUnixNano.Load()
	if windowStart == 0 || now.Sub(time.Unix(0, windowStart)) >= openAIOAuth429StormWindow {
		return false
	}
	return s.openaiOAuth429WindowCount.Load() >= openAIOAuth429StormThreshold
}

// tkOpenAIOAuth429AccountAttemptBudget returns how many OpenAI OAuth accounts
// one request may try after 429s. During a pool-wide storm each extra switch
// only adds another upstream 429, so the budget drops to one switch.
func (s *OpenAIGatewayService) tkOpenAIOAuth429AccountAttemptBudget() int {
	if s.openAIOAuth429StormActive(time.Now()) {
		return 1 + openAIOAuth429StormMaxAccountSwitches
	}
	return openAIOAuth429MaxAccountAttempts
}
