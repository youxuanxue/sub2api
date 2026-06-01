package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Anthropic Usage-Policy / cyber-safeguard classification.
//
// Anthropic runs a safety/usage-policy classifier that can reject a request with
// a distinctive message rather than a normal rate-limit / auth error, e.g.:
//
//	"Claude Code is unable to respond to this request, which appears to violate
//	 our Usage Policy (...). This request triggered cyber-related safeguards.
//	 To request an adjustment ... Cyber Verification Program ..."
//
// (claude-code#60366, #61625, #61828). Two facts shape how TokenKey must treat
// this class of error:
//
//  1. It is a RISK SIGNAL, not generic jitter. An account that starts getting
//     policy-blocked is being looked at by Anthropic's classifier; ops needs to
//     see that distinctly from a 429/529 blip.
//  2. It is frequently a FALSE POSITIVE — benign prompts (even "hi") trip it, and
//     it often clears on its own. So it must NOT permanently disable the account,
//     and it must NOT advance the generic anthropic 3/3 error ladder (which would
//     cool a healthy account for up to 10 minutes on a benign request).
//
// The chosen behavior: emit a dedicated `anthropic_usage_policy_block` signal,
// apply a SHORT de-prioritization so a *persistently* flagged account is routed
// around, and fail the in-flight request over — without touching the harsh
// ladder. The cooldown is deliberately short because the block is often
// content-induced rather than account-induced: a long cooldown would punish a
// healthy account for one unlucky prompt, while a short one still lets the
// scheduler skip an account that is being flagged repeatedly.
const anthropicUsagePolicyCooldown = 60 * time.Second

// usagePolicyBlockMarkers are lowercase substrings that, when present in an
// Anthropic upstream error, identify a usage-policy / cyber-safeguard block.
// Kept tight to avoid matching unrelated text.
var usagePolicyBlockMarkers = []string{
	"violate our usage policy",
	"violates our usage policy",
	"cyber-related safeguard",
	"cyber verification program",
	"high-risk cyber",
}

// tkIsAnthropicUsagePolicyBlock reports whether an Anthropic upstream error is a
// usage-policy / cyber-safeguard classifier block.
func tkIsAnthropicUsagePolicyBlock(upstreamMsg string, responseBody []byte) bool {
	hay := strings.ToLower(upstreamMsg)
	if len(responseBody) > 0 {
		hay += " " + strings.ToLower(string(responseBody))
	}
	for _, m := range usagePolicyBlockMarkers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// handleAnthropicUsagePolicyBlock records the risk signal and applies a short
// de-prioritization, returning true so the in-flight request fails over. It
// deliberately does NOT advance the generic anthropic error counter / 3-strike
// tier ladder and does NOT permanently disable the account.
func (s *RateLimitService) handleAnthropicUsagePolicyBlock(ctx context.Context, account *Account, statusCode int, upstreamMsg string, responseBody []byte) (shouldDisable bool) {
	// The dedicated risk signal ops watches for. A rising rate of this across
	// accounts means Anthropic's classifier is actively flagging our cohort.
	slog.Warn("anthropic_usage_policy_block",
		"account_id", account.ID,
		"account_name", account.Name,
		"status_code", statusCode,
		"pool_mode", account.IsPoolMode(),
		"message", upstreamMsg)

	now := time.Now()
	until := now.Add(anthropicUsagePolicyCooldown)
	reasonMessage := fmt.Sprintf("Anthropic usage-policy / cyber-safeguard block (%d, cooldown=%s): %s",
		statusCode, anthropicUsagePolicyCooldown, upstreamMsg)
	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      statusCode,
		MatchedKeyword:  "anthropic_usage_policy_block",
		RuleIndex:       -1,
		ErrorMessage:    truncateTempUnschedMessage([]byte(reasonMessage), tempUnschedMessageMaxBytes),
	}
	reason := reasonMessage
	if raw, marshalErr := json.Marshal(state); marshalErr == nil {
		reason = string(raw)
	}
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("anthropic_usage_policy_block_set_temp_unschedulable_failed",
			"account_id", account.ID, "status_code", statusCode, "error", err)
		// Even if the cooldown write fails, fail this request over.
		return true
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
			slog.Warn("anthropic_usage_policy_block_temp_unsched_cache_set_failed",
				"account_id", account.ID, "error", err)
		}
	}
	return true
}
