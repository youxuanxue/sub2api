package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// OpenAIImplicitThrottleCooldownSeconds returns the opt-in cooldown (seconds)
// applied to an OpenAI-compat account that the upstream is implicitly throttling
// (repeated 5xx / header-timeout with no explicit 429). Returns 0 — disabled —
// when unset, blank, non-numeric, or negative.
//
// TK (upstream Wei-Shaw/sub2api#2727).
func (s *SettingService) OpenAIImplicitThrottleCooldownSeconds(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return 0
	}
	vals, err := s.settingRepo.GetMultiple(ctx, []string{SettingKeyOpenAIImplicitThrottleCooldownSeconds})
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(vals[SettingKeyOpenAIImplicitThrottleCooldownSeconds]))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// tkApplyImplicitThrottleCooldown briefly benches an OpenAI-compat account after
// a failover-worthy upstream 5xx so that subsequent requests skip it instead of
// repeatedly landing on the same implicitly-throttled account.
//
// TK (upstream Wei-Shaw/sub2api#2727): an account being implicitly throttled by
// the upstream often hangs until the response-header timeout and then returns a
// 502 WITHOUT an explicit 429. The non-Anthropic branch of HandleUpstreamError
// only logs such 5xx (shouldDisable=false), so the account stays schedulable and
// the next request can pick it again — "请求反复打到同一个已经限流的账户上去".
// Within a single request the failover loop already excludes the failed account
// via FailedAccountIDs; this closes the CROSS-request gap.
//
// Strictly opt-in: the cooldown is 0 (disabled) by default, so merging this
// changes no production behavior until an operator sets
// SettingKeyOpenAIImplicitThrottleCooldownSeconds > 0. We intentionally do NOT
// change the deliberately-unbounded OpenAIResponseHeaderTimeout default (its
// 0=unlimited value is covered by TestLoadDefaultOpenAIResponseHeaderTimeoutUnlimited
// and is required for long Codex streaming sessions).
func (s *OpenAIGatewayService) tkApplyImplicitThrottleCooldown(ctx context.Context, account *Account, statusCode int) {
	if account == nil || s.settingService == nil || s.accountRepo == nil {
		return
	}
	// Only transient capacity 5xx (the implicit-throttle signature). Auth/ban/
	// rate-limit statuses (401/403/429) are < 500 and excluded; 529 (overload)
	// is explicitly excluded too because handle529 already writes an authoritative
	// SetOverloaded cooldown — benching it again here would double-write a
	// less-precise window over it.
	if statusCode < 500 || statusCode == 529 {
		return
	}
	seconds := s.settingService.OpenAIImplicitThrottleCooldownSeconds(ctx)
	if seconds <= 0 {
		return
	}
	until := time.Now().Add(time.Duration(seconds) * time.Second)
	reason := fmt.Sprintf("OpenAI implicit-throttle cooldown (%ds) after upstream %d (TK#2727)", seconds, statusCode)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("openai_implicit_throttle_cooldown_failed",
			"account_id", account.ID, "status_code", statusCode, "error", err)
		return
	}
	slog.Info("openai_implicit_throttle_cooldown_applied",
		"account_id", account.ID, "status_code", statusCode, "cooldown_seconds", seconds)
}
