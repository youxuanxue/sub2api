package service

import (
	"context"
	"log/slog"
)

// TK — sticky-side completion of the anthropic saturated mirror-stub
// de-prioritization (see gateway_service_tk_saturation_penalty.go for the
// Layer-2 load-balance score side and ratelimit_service_tk_saturation.go for
// the increment side + the prod problem statement).
//
// Problem (prod, 2026-06-19 evidence): prod reaches Anthropic only through
// per-edge apikey mirror "stub" accounts (cc-us5 …) that forward to a SPOF
// edge. When that edge's single OAuth account is upstream-rate-limited, the
// edge returns TokenKey's own "No available accounts" 429; HandleUpstreamError
// correctly SKIPS the cooldown ladder (counting it once caused the 2026-05-31
// "503 amplifier") and instead feeds the bounded saturation counter. The
// Layer-2 load-balance ranking then de-prioritizes the saturated stub. BUT the
// Layer-1.5 sticky block in SelectAccountWithLoadAwareness honors a bound stub
// via shouldClearStickySession, which only inspects
// IsSchedulable()+model-rate-limit — both stay true for a saturated stub by
// design. So a session bound to a dead edge keeps selecting it FIRST and paying
// a wasted failover hop on every request, with zero prompt-cache benefit (the
// request fails over to a DIFFERENT edge, so affinity to the dead stub is
// already worthless). prod 24h: cc-us5 alone took 1590 such hits.
//
// tkShouldClearStickyForSaturation augments the clearSticky decision in the
// Layer-1.5 block ONLY: when the bound anthropic stub is SUSTAINEDLY saturated
// (live in-window count >= anthropicSaturationThreshold), the binding is cleared
// so the request falls through to Layer-2, where computeAnthropicSaturationPenalties
// applies the +1000 penalty and selection lands on a healthy sibling that is then
// re-bound. It is deliberately NOT wired into the legacy
// selectAccountForModelWithPlatform / selectAccountWithMixedScheduling sticky
// blocks (count_tokens failover / LoadBatchEnabled=false): those re-select by raw
// priority with NO saturation penalty, so clearing there could re-pick the SAME
// saturated stub and re-bind it, churning the binding + this log for no benefit.
// It is a routing PREFERENCE, NOT a cooldown — it reuses the
// SAME clear+fall-through mechanism the existing unschedulable check already
// triggers, never calls SetTempUnschedulable / SetRateLimited / advances the
// 3/3 ladder, and is self-clearing (the counter has a ~90s TTL, so once the
// edge recovers the binding can re-form). Amplifier-safe: clearing a sticky
// binding never removes the stub from the candidate set; if it is the only
// candidate the load-balance path still selects it.
//
// Deliberately gated tight so steady-state prompt-cache affinity is untouched:
// only platform=anthropic, only when the de-prioritize feature is enabled
// (shared kill-switch SettingKeyAnthropicSaturatedStubDeprioritizeEnabled), and
// only at/above the same SUSTAINED threshold (4) the Layer-2 penalty uses — so a
// transient blip (1-3 hits/90s) never clears a binding. Best-effort: a nil
// counter, disabled feature, or any Redis error leaves the binding untouched
// (selection must never fail because the preference counter is unavailable).
//
// Logs (Info) exactly when it decides to clear — the existing sticky.* logs are
// slog.Debug (off in prod), so this single low-volume line (≈ once per affected
// session per saturation episode, since the cleared binding is not re-entered)
// is how ops sees the sticky side engage.
func (s *GatewayService) tkShouldClearStickyForSaturation(ctx context.Context, account *Account, sessionHash string) bool {
	if s == nil || s.tkAnthropicSaturationCounter == nil || account == nil {
		return false
	}
	if account.Platform != PlatformAnthropic {
		return false
	}
	if s.settingService != nil && !s.settingService.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx) {
		return false
	}
	counts, err := s.tkAnthropicSaturationCounter.GetSaturationBatch(ctx, []int64{account.ID})
	if err != nil {
		return false
	}
	count := counts[account.ID]
	if count < anthropicSaturationThreshold {
		return false
	}
	slog.Info("anthropic_sticky_cleared_saturated_stub",
		"account_id", account.ID,
		"recent_count", count,
		"threshold", anthropicSaturationThreshold,
		"window_seconds", anthropicSaturationWindowSeconds,
		"session", shortSessionHash(sessionHash),
	)
	return true
}
