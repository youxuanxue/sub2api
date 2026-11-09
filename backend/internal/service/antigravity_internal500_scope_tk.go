package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// INTERNAL 500 ("Internal error encountered.", status INTERNAL) is a transient
// Google-side failure of one model endpoint, not an account fault. The upstream
// ladder (antigravity_internal500_penalty.go) parks the whole account for
// 30 min, then 2 h, then SetError, whatever model failed, which takes every
// other model on a healthy account out of a small Antigravity pool. Upstream
// #8521 narrowed this for image models only; TK narrows it for every model the
// request resolves to.
const (
	// Mirrors upstream #8521's image-model default: long enough that the next
	// few requests fail over instead of re-hitting the failing endpoint, short
	// enough that a recovered model is back before a client gives up.
	antigravityInternal500ModelCooldown       = 2 * time.Minute
	antigravityInternal500ModelCooldownReason = "antigravity_internal500_model"
)

// handleInternal500ForModel handles an INTERNAL 500 that survived every
// retry. The name and call site match upstream #8521 so the retry loop does
// not conflict on merge; upstream defines a narrower image-only version in
// antigravity_internal500_penalty.go, and the merge must drop that one. It cools only the exact final model on this account and never
// touches account status, temp-unschedulable or the account INTERNAL 500
// counter. Only when the model key cannot be resolved does it fall back to the
// upstream account ladder, so the failure is never silently dropped.
func (s *AntigravityGatewayService) handleInternal500ForModel(
	ctx context.Context, prefix string, account *Account, modelKey string,
) {
	modelKey = strings.TrimSpace(modelKey)
	if account == nil || modelKey == "" || s.accountRepo == nil {
		s.handleInternal500RetryExhausted(ctx, prefix, account)
		return
	}
	until := time.Now().Add(antigravityInternal500ModelCooldown)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, until, antigravityInternal500ModelCooldownReason); err != nil {
		slog.Error("internal500_model_cooldown_failed",
			"prefix", prefix, "account_id", account.ID, "model", modelKey, "error", err)
		return
	}
	s.updateAccountModelRateLimitInCache(ctx, account, modelKey, until)
	slog.Info("internal500_model_cooldown",
		"account_id", account.ID, "account_name", account.Name, "model", modelKey,
		"duration", antigravityInternal500ModelCooldown)
}
