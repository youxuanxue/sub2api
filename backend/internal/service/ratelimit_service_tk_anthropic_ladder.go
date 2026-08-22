package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	// Built-in defaults for handleAnthropicUpstreamError; the live values
	// are read via getAnthropicErrorThreshold / getAnthropicErrorWindowMinutes
	// so operators can lift the threshold for single-account or small-pool
	// deployments without recompiling.
	anthropicUpstreamErrorThresholdDefault     = 3
	anthropicUpstreamErrorWindowMinutesDefault = 1

	// Cooldown escalation TTL: how long a prior cooldown trigger keeps the
	// account at an elevated tier before falling back to the shortest tier
	// (30s). Anything inside this window counts toward escalation.
	anthropicCooldownTierTTLMinutes = 30

	// Window owned by the global "tier >= 1" escalation counter that drives
	// the anthropic_cooldown_tier_escalation_count ops_alert_evaluator
	// metric. 60 min picks the smallest unit operators care about for the
	// "is the whole pool burning down right now" question; the counter
	// expires when the window closes so a healed deploy reads zero.
	anthropicCooldownTierEscalationsWindowMinutes = 60
)

// openAICloudflareChallengeKeywords matches Cloudflare / Arkose challenge
// pages where an OpenAI 403 was returned by infrastructure (CF JS challenge,
// Arkose FunCaptcha) rather than by OpenAI's auth/permission layer. The
// most common trigger today is the OAuth /v1/images/{generations,edits}
// path under heavy automation — Cherry Studio and similar clients see a
// per-request CF challenge HTML body, but the OAuth identity itself is
// healthy. Treating these as account-level 403s would write a 10-minute
// temp_unschedulable on the FIRST hit and SetError on the 3rd within the
// 180-min counter window, removing the OAuth account from the pool for ALL
// non-image traffic too. Body match is path-agnostic on purpose: a real
// OpenAI 403 returns structured JSON ({"error":{"code":"...","message":..."}}),
// none of these keywords appear in legitimate auth/permission errors.
//
// Matching is case-insensitive; order is insignificant.
// See upstream Wei-Shaw/sub2api#1824 and #2413.
var openAICloudflareChallengeKeywords = []string{
	"cloudflare",
	"just a moment",
	"arkoselabs",
	"funcaptcha",
	"challenge-platform",
}

// getAnthropicErrorThreshold returns the configured 3/3-style threshold, or
// the built-in default when unset / zero / negative.
func (s *RateLimitService) getAnthropicErrorThreshold() int64 {
	if s != nil && s.cfg != nil && s.cfg.RateLimit.AnthropicErrorThreshold > 0 {
		return int64(s.cfg.RateLimit.AnthropicErrorThreshold)
	}
	return anthropicUpstreamErrorThresholdDefault
}

// getAnthropicErrorWindowMinutes returns the configured short-window length
// for the 3/3 counter, or the built-in default when unset / zero / negative.
func (s *RateLimitService) getAnthropicErrorWindowMinutes() int {
	if s != nil && s.cfg != nil && s.cfg.RateLimit.AnthropicErrorWindowMinutes > 0 {
		return s.cfg.RateLimit.AnthropicErrorWindowMinutes
	}
	return anthropicUpstreamErrorWindowMinutesDefault
}

// anthropicCooldownTierLadder picks an exponentially longer cooldown when
// the same account repeatedly trips the 3/3 short-window threshold inside
// anthropicCooldownTierTTLMinutes. Tier index = (recent cooldown count - 1)
// clamped to len-1.
//
// Tier 0 (first hit in 30 min): 30s — transient upstream jitter
// Tier 1 (second hit):           2 min — repeat suggests real problem
// Tier 2+ (third+ hit):          10 min — persistent failure, back off hard
//
// Replaces the prior fixed 10-min cooldown which amplified single transient
// bursts into 10-min group outages on single-member exclusive groups
// (2026-05-21 cc-us1-oauth → cc-edges incident). The shortest tier is the
// dominant case; the long tail still escalates to give upstream room.
var anthropicCooldownTierLadder = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

// anthropicCooldownEscalationSlotMaxSeconds is the placeholder TTL a threshold
// trip uses to win the per-account escalation slot before its real cooldown is
// known. It equals the longest ladder cooldown so that a crash between winning
// the slot and shrinking it to the real cooldown can over-suppress escalation
// by at most one max-cooldown window — never under-protect. See issue #623 and
// AnthropicUpstreamErrorCounterCache.AcquireAnthropicCooldownEscalationSlot.
var anthropicCooldownEscalationSlotMaxSeconds = int(anthropicCooldownTierLadder[len(anthropicCooldownTierLadder)-1].Seconds())

// handleAnthropicUpstreamError counts upstream infra-health errors per account
// in a 1-min short window and, on the 3rd hit, marks the account
// temp_unschedulable. Only status codes passing tkAnthropicStubHealthFuseEligible
// reach this path (502/503/504/529, plus 429 only after SetRateLimited).
//
// Pool-mode policy (2026-05-21 revision of PR #333, which itself reversed
// PR #248): pool_mode accounts are NOT bypassed here. PR #333's blanket
// immunity meant a pool_mode account would persistently take traffic even
// when its upstream pool was genuinely down — the slog-only signal had no
// alert hook and the failover loop alone could not protect a single-member
// exclusive group from cascading customer-facing 503s.
//
// The new design keeps the 3/3 threshold for everyone, but the cooldown
// itself is exponential rather than a fixed 10 min:
//   - first cooldown in 30 min:  30s (transient jitter shrugged off)
//   - second cooldown in 30 min: 2 min
//   - third+ cooldown:           10 min (persistent failure, hard back-off)
//
// This restores the mechanical "account is failing" signal that ops
// dashboards / on-call can act on, while limiting customer-visible outage
// on transient upstream jitter to 30 seconds on the first hit. Pool_mode
// retains its meaningful semantic (one in-place same-account retry via
// isPoolModeRetryableStatus + GetPoolModeRetryCount) — that retry already
// absorbs most single-shot upstream pool jitter before the 3/3 counter
// can fire.
//
// Other Anthropic protections (credit-balance / KYC / organization-disabled
// in case 400, OAuth 401 refresh, 429 retry-after cooldown, 529 overload)
// live outside this function and are unaffected.
func (s *RateLimitService) handleAnthropicUpstreamError(ctx context.Context, account *Account, statusCode int, upstreamMsg string, responseBody []byte) (shouldDisable bool) {
	if tkIsKiroMirrorStub(account) && tkIsKiroEndpointQuotaExhausted(upstreamMsg, responseBody) {
		return s.tkHandleKiroEndpointQuotaExhausted(ctx, account, upstreamMsg)
	}
	// TK (prod incident 2026-07-20): Kiro edges wrap their exhausted upstream
	// request path in a stable TokenKey 502 "Upstream service temporarily
	// unavailable" envelope. The prod Kiro account is only a relay stub, so
	// advancing its Anthropic 3/3 health fuse duplicates the edge failure and
	// can cool the entire Kiro mirror pool. Fail over and retain bounded
	// saturation preference, but leave relay health untouched. Raw infra 502s
	// do not match the parsed message and continue through the fuse below.
	if tkSkipDownstreamKiroServiceUnavailablePenalty(account, statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_kiro_service_unavailable_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "kiro_service_unavailable")
		return true
	}
	// TK (prod incident 2026-05-31): a 503 whose body is the downstream gateway's
	// own "no available accounts" pool-exhaustion signal is a transient capacity
	// blip on the *forwarded-to* pool (e.g. a thin edge bursting on parallel haiku
	// background requests), NOT a health problem with THIS forwarding stub account.
	// Advancing the per-account anthropic_upstream_error counter would let a 3-503
	// edge burst trip the 3/3 ladder and cool the whole edge stub for 10 minutes
	// (tier=2), collapsing the prod pool — a self-inflicted 503 amplifier. Fail the
	// in-flight request over to the next stub (return true) but leave stub health
	// untouched: no counter advance, no SetTempUnschedulable. See
	// tkIsDownstreamNoAvailableAccounts.
	if tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_no_available_accounts_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		// TK: feed the bounded saturation de-prioritization preference (legacy 503
		// path). Not a cooldown — ladder/SetTempUnschedulable stay untouched.
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "no_available_accounts")
		return true
	}

	// TK (G2, narrow): sibling downstream capacity signal. A forwarded
	// "all available accounts exhausted" envelope (HTTP 502 from a downstream
	// gateway's failover-exhausted path) is a downstream-pool blip, not stub
	// health — fail over without advancing the 3/3 ladder. Matches ONLY TokenKey's
	// own capacity phrase, so raw edge-infra 5xx and genuine provider errors still
	// count and keep the route-away cooldown (PR #333). See
	// tkSkipDownstreamFailoverExhaustedPenalty.
	if tkSkipDownstreamFailoverExhaustedPenalty(statusCode, upstreamMsg, responseBody) {
		slog.Info("anthropic_downstream_failover_exhausted_skip_penalty",
			"account_id", account.ID,
			"status_code", statusCode)
		// TK: feed the bounded saturation de-prioritization preference.
		s.recordAnthropicStubSaturation(ctx, account.ID, statusCode, "all_available_accounts_exhausted")
		return true
	}

	return s.handleAnthropicUpstreamErrorWithOptions(ctx, account, statusCode, upstreamMsg, responseBody, false)
}

// handleAnthropicUpstreamErrorWithOptions is the implementation seam for
// callers that already wrote an authoritative cooldown to the account
// (handle429 with retry-after / handle529 with overload_until). When
// skipCooldownWrite=true the 3/3 + tier counters still advance and the
// observability slog still fires — only the SetTempUnschedulable write
// is suppressed so the just-written rate_limit_reset / overload_until is
// not raced by a less-precise ladder cooldown (last-write-wins).
func (s *RateLimitService) handleAnthropicUpstreamErrorWithOptions(ctx context.Context, account *Account, statusCode int, upstreamMsg string, responseBody []byte, skipCooldownWrite bool) (shouldDisable bool) {
	if !tkAnthropicStubHealthFuseEligible(statusCode, skipCooldownWrite) {
		return false
	}
	msg := buildAnthropicUpstreamErrorMessage(statusCode, upstreamMsg, responseBody)
	if s.anthropicUpstreamErrorCounterCache == nil {
		slog.Warn("anthropic_upstream_error_counter_missing", "account_id", account.ID, "status_code", statusCode)
		return false
	}

	threshold := s.getAnthropicErrorThreshold()
	windowMinutes := s.getAnthropicErrorWindowMinutes()

	count, err := s.anthropicUpstreamErrorCounterCache.IncrementAnthropicUpstreamErrorCount(ctx, account.ID, windowMinutes)
	if err != nil {
		slog.Warn("anthropic_upstream_error_increment_failed", "account_id", account.ID, "status_code", statusCode, "error", err)
		return false
	}
	if count < threshold {
		slog.Warn("anthropic_upstream_error_count",
			"account_id", account.ID,
			"status_code", statusCode,
			"count", count,
			"threshold", threshold,
			"pool_mode", account.IsPoolMode())
		return false
	}

	// Escalation slot (issue #623): only escalate the tier / (re)apply a cooldown
	// once per *failure episode*. A single fast burst — e.g. an edge rolling-
	// upgrade swap window throwing several 503s in a few seconds — otherwise
	// re-runs this block per error and climbs 30s → 2min → 10min within seconds,
	// even though errors #2..n are racing in-flight requests from the SAME
	// episode that error #1 already cooled the account for. We win the slot atomically;
	// a loser fails the in-flight request over (return true) WITHOUT advancing the
	// tier or rewriting the cooldown. The slot auto-clears when the cooldown would
	// have expired (shrunk below), so a genuine re-trip after the account is
	// rescheduled is a new episode and escalates again — matching the ladder's
	// documented "repeatedly trips ... inside 30 min" intent. Best-effort: on a
	// Redis error we fall through and escalate as before so a persistent failure is
	// never under-protected by a guard outage.
	acquiredSlot := false
	if won, slotErr := s.anthropicUpstreamErrorCounterCache.AcquireAnthropicCooldownEscalationSlot(ctx, account.ID, anthropicCooldownEscalationSlotMaxSeconds); slotErr != nil {
		slog.Warn("anthropic_cooldown_escalation_slot_acquire_failed",
			"account_id", account.ID,
			"status_code", statusCode,
			"error", slotErr)
	} else if !won {
		slog.Info("anthropic_upstream_error_escalation_suppressed_active_cooldown",
			"account_id", account.ID,
			"status_code", statusCode,
			"count", count,
			"threshold", threshold,
			"pool_mode", account.IsPoolMode())
		return true
	} else {
		acquiredSlot = true
	}

	// Pick the cooldown duration based on how many times this same account
	// has tripped the threshold inside the last anthropicCooldownTierTTLMinutes.
	// Best-effort: any Redis failure falls back to the shortest tier so we
	// never accidentally apply a 10-min cooldown on a counter error.
	tierIndex := 0
	tierCount, tierErr := s.anthropicUpstreamErrorCounterCache.IncrementAnthropicCooldownTier(ctx, account.ID, anthropicCooldownTierTTLMinutes)
	if tierErr != nil {
		slog.Warn("anthropic_cooldown_tier_increment_failed",
			"account_id", account.ID,
			"status_code", statusCode,
			"error", tierErr)
	} else if tierCount > 1 {
		tierIndex = int(tierCount) - 1
		if tierIndex >= len(anthropicCooldownTierLadder) {
			tierIndex = len(anthropicCooldownTierLadder) - 1
		}
	}
	cooldown := anthropicCooldownTierLadder[tierIndex]

	// Shrink the escalation slot to exactly this cooldown so the NEXT escalation
	// can only happen after the account would have been rescheduled. Best-effort.
	if acquiredSlot {
		if ttlErr := s.anthropicUpstreamErrorCounterCache.SetAnthropicCooldownEscalationSlotTTL(ctx, account.ID, int(cooldown.Seconds())); ttlErr != nil {
			slog.Warn("anthropic_cooldown_escalation_slot_ttl_failed",
				"account_id", account.ID,
				"status_code", statusCode,
				"cooldown_seconds", int(cooldown.Seconds()),
				"error", ttlErr)
		}
	}

	// Emit a global "tier >= 1" escalation signal so ops_alert_evaluator can
	// surface "persistent failure rising" without scanning every per-account
	// counter. tier=0 is intentionally silent — that's transient jitter and
	// the 30s cooldown absorbs it. Failure here only loses telemetry.
	if tierIndex >= 1 {
		if _, escErr := s.anthropicUpstreamErrorCounterCache.IncrementAnthropicCooldownTierEscalations(ctx, anthropicCooldownTierEscalationsWindowMinutes); escErr != nil {
			slog.Warn("anthropic_cooldown_tier_escalation_increment_failed",
				"account_id", account.ID,
				"status_code", statusCode,
				"tier", tierIndex,
				"error", escErr)
		} else {
			slog.Warn("anthropic_cooldown_tier_escalation",
				"account_id", account.ID,
				"status_code", statusCode,
				"tier", tierIndex,
				"cooldown_seconds", int(cooldown.Seconds()),
				"pool_mode", account.IsPoolMode())
		}
	}

	if skipCooldownWrite {
		// The dispatch caller (case 429 retry-after path / case 529 overload
		// path) already wrote an authoritative cooldown to the account
		// state. Suppress the ladder's SetTempUnschedulable write so the
		// just-landed rate_limit_reset / overload_until is not overwritten
		// by a less-precise local cooldown. Counters above still advanced.
		slog.Warn("anthropic_upstream_error_cooldown_write_skipped",
			"account_id", account.ID,
			"status_code", statusCode,
			"count", count,
			"threshold", threshold,
			"cooldown_seconds", int(cooldown.Seconds()),
			"tier", tierIndex,
			"reason", "authoritative_cooldown_already_written",
			"pool_mode", account.IsPoolMode())
		return true
	}

	now := time.Now()
	until := now.Add(cooldown)
	reasonMessage := fmt.Sprintf("Anthropic upstream error threshold (%d/%d, cooldown=%s tier=%d): %s",
		count, threshold, cooldown, tierIndex, msg)
	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      statusCode,
		MatchedKeyword:  "anthropic_upstream_error",
		RuleIndex:       -1,
		ErrorMessage:    truncateTempUnschedMessage([]byte(reasonMessage), tempUnschedMessageMaxBytes),
	}
	reason := reasonMessage
	if raw, marshalErr := json.Marshal(state); marshalErr == nil {
		reason = string(raw)
	}
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("anthropic_upstream_error_set_temp_unschedulable_failed", "account_id", account.ID, "status_code", statusCode, "error", err)
		return true
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
			slog.Warn("anthropic_upstream_error_temp_unsched_cache_set_failed", "account_id", account.ID, "error", err)
		}
	}

	// Single observability anchor for ops alerts: cooldown_seconds and tier
	// let evaluators distinguish "transient jitter shrugged off" (tier=0,
	// 30s) from "persistent failure escalating" (tier>=2, 10min).
	slog.Warn("anthropic_upstream_error_temp_unschedulable",
		"account_id", account.ID,
		"status_code", statusCode,
		"until", until,
		"count", count,
		"threshold", threshold,
		"cooldown_seconds", int(cooldown.Seconds()),
		"tier", tierIndex,
		"pool_mode", account.IsPoolMode())
	return true
}

func buildAnthropicUpstreamErrorMessage(statusCode int, upstreamMsg string, responseBody []byte) string {
	return buildForbiddenErrorMessage(
		fmt.Sprintf("Anthropic upstream error (%d):", statusCode),
		upstreamMsg,
		responseBody,
		"upstream request failed",
	)
}

func (s *RateLimitService) handle404(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) (shouldDisable bool) {
	if account.Platform != PlatformAnthropic {
		return false
	}
	if !IsAnthropicModelNotFound404(responseBody, upstreamMsg) {
		return false
	}
	// TK (prod P0 2026-06-06, edge us5): an Anthropic 404 model-not-found is a
	// CLIENT error (a model name no Anthropic account can serve — the catalog is
	// global, not per-account), so cooling account×model only drains a thin pool
	// into "No available accounts" 429s. Skip the penalty here too — this is the
	// fall-through gate reached when HandleUpstreamModelNotFound is bypassed
	// (no requestedModel at the call site). See the same rationale on
	// HandleUpstreamModelNotFound; the gateway error mapping surfaces a 400
	// invalid_request to the client. shouldDisable=false keeps the account
	// schedulable and avoids wrapping the 404 as an UpstreamFailoverError.
	slog.Info("anthropic_model_not_found_skip_penalty",
		"account_id", account.ID,
		"requested_model", strings.TrimSpace(extractAnthropicNotFoundModel(responseBody, upstreamMsg)))
	return false
}

