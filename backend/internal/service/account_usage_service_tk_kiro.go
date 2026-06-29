package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// KiroUsageInfo is the kiro (CodeWhisperer) credits/subscription snapshot surfaced
// in UsageInfo. Unlike anthropic/openai's rolling 5h/7d windows, kiro exposes a
// credits budget (Current/Limit) that resets on a monthly date, plus an optional
// free-trial allowance with its own expiry. Percent is 0-100 to match
// UsageProgress.Utilization so the edge DTO can render it as a window bar.
type KiroUsageInfo struct {
	Current           float64        `json:"current,omitempty"`
	Limit             float64        `json:"limit,omitempty"`
	Percent           float64        `json:"percent,omitempty"` // 0-100
	NextResetDate     string         `json:"next_reset_date,omitempty"`
	SubscriptionTitle string         `json:"subscription_title,omitempty"`
	Trial             *KiroTrialInfo `json:"trial,omitempty"`
}

// KiroTrialInfo is the kiro free-trial allowance (present only while a trial is
// active). ExpiresAt is the trial end, distinct from the monthly credits reset.
type KiroTrialInfo struct {
	Current   float64    `json:"current,omitempty"`
	Limit     float64    `json:"limit,omitempty"`
	Percent   float64    `json:"percent,omitempty"` // 0-100
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// getKiroUsage returns the kiro credits/subscription/trial snapshot. The「仅按需
// 刷新」invariant is enforced HERE, at the source, independent of caller:
//
//   - force=false (page load, auto-refresh, any default usage read): returns the
//     PASSIVE snapshot rebuilt from Account.Extra. It NEVER touches CodeWhisperer,
//     so refreshing the accounts page can never trigger an upstream kiro call.
//   - force=true (the operator's explicit「查询」): calls the vendored
//     RefreshAccountInfo (GetUsageLimits) once, maps it onto UsageInfo.KiroUsage,
//     and writes it back to passive Extra so subsequent passive reads (incl. the
//     edge overview's GetPassiveUsageBatch) render without another upstream call.
//
// kiro has no「请求响应头顺带刷新」path (anthropic/openai refresh their windows
// from rate-limit headers on every gateway request; kiro reports credits only via
// the separate GetUsageLimits control-plane call), so an unforced auto-fetch would
// be the ONLY thing hitting upstream on a page render — exactly what we forbid.
// singleflight dedups truly-concurrent 查询 clicks; there is intentionally no
// positive cache, so each explicit 查询 returns fresh data.
func (s *AccountUsageService) getKiroUsage(ctx context.Context, account *Account, force bool) (*UsageInfo, error) {
	now := time.Now()
	if account == nil {
		return &UsageInfo{UpdatedAt: &now}, nil
	}

	// Passive baseline from persisted Extra (no upstream call). Returned as-is on a
	// non-forced read, and reused as the degraded fallback when a forced fetch fails.
	passive := s.buildPassiveKiroUsage(account)
	if !force {
		enrichUsageWithAccountError(passive, account)
		return passive, nil
	}

	flightKey := fmt.Sprintf("kiro-usage:%d", account.ID)
	result, flightErr, _ := s.cache.kiroFlight.Do(flightKey, func() (any, error) {
		kiroAcct := account.toKiroProtoAccount()
		info, err := kiroproto.RefreshAccountInfo(kiroAcct)
		if err != nil {
			slog.Warn("kiro usage fetch failed, returning degraded response", "account_id", account.ID, "error", err)
			passive.Error = fmt.Sprintf("usage API error: %v", err)
			enrichUsageWithAccountError(passive, account)
			return passive, nil
		}

		s.persistKiroProfileArnIfChanged(ctx, account, kiroAcct)

		usage := buildKiroUsageFromInfo(info)
		s.syncKiroActiveToPassive(ctx, account.ID, usage)
		enrichUsageWithAccountError(usage, account)
		return usage, nil
	})
	if flightErr != nil {
		return nil, flightErr
	}
	usage, ok := result.(*UsageInfo)
	if !ok || usage == nil {
		return passive, nil
	}
	return usage, nil
}

// persistKiroProfileArnIfChanged writes a freshly resolved profile_arn back to account
// credentials so subsequent usage/gateway calls do not repeat ListAvailableProfiles or
// keep sending a stale ARN that triggers HTTP 400 Invalid profileArn.
func (s *AccountUsageService) persistKiroProfileArnIfChanged(ctx context.Context, account *Account, kiroAcct *kiroproto.Account) {
	if s == nil || account == nil || kiroAcct == nil {
		return
	}
	resolved := strings.TrimSpace(kiroAcct.ProfileArn)
	if resolved == "" || resolved == account.GetKiroProfileArn() {
		return
	}
	merged := MergeCredentials(account.Credentials, map[string]any{"profile_arn": resolved})
	if err := persistAccountCredentials(ctx, s.accountRepo, account, merged); err != nil {
		slog.Warn("persist_kiro_profile_arn_failed", "account_id", account.ID, "error", err)
	}
}

// buildKiroUsageFromInfo maps the vendored kiro.AccountInfo (GetUsageLimits result)
// onto an active UsageInfo. Percent fields are scaled 0-1 → 0-100.
func buildKiroUsageFromInfo(info *kiroproto.AccountInfo) *UsageInfo {
	now := time.Now()
	usage := &UsageInfo{Source: "active", UpdatedAt: &now}
	if info == nil {
		return usage
	}
	ku := &KiroUsageInfo{
		Current:           info.UsageCurrent,
		Limit:             info.UsageLimit,
		Percent:           info.UsagePercent * 100,
		NextResetDate:     info.NextResetDate,
		SubscriptionTitle: info.SubscriptionTitle,
	}
	if info.TrialUsageLimit > 0 || info.TrialExpiresAt > 0 || info.TrialStatus != "" {
		trial := &KiroTrialInfo{
			Current: info.TrialUsageCurrent,
			Limit:   info.TrialUsageLimit,
			Percent: info.TrialUsagePercent * 100,
			Status:  info.TrialStatus,
		}
		if info.TrialExpiresAt > 0 {
			t := time.Unix(info.TrialExpiresAt, 0)
			trial.ExpiresAt = &t
		}
		ku.Trial = trial
	}
	usage.KiroUsage = ku
	return usage
}

// syncKiroActiveToPassive persists the active kiro snapshot to Account.Extra so the
// next passive load (and the edge overview's GetPassiveUsageBatch) sees it without a
// fresh upstream call. Mirrors syncActiveToPassive's contract.
func (s *AccountUsageService) syncKiroActiveToPassive(ctx context.Context, accountID int64, usage *UsageInfo) {
	if usage == nil || usage.KiroUsage == nil {
		return
	}
	ku := usage.KiroUsage
	updates := map[string]any{
		"kiro_usage_current":      ku.Current,
		"kiro_usage_limit":        ku.Limit,
		"kiro_usage_percent":      ku.Percent,
		"kiro_usage_sampled_at":   time.Now().UTC().Format(time.RFC3339),
		"kiro_next_reset":         nil,
		"kiro_subscription_title": nil,
		"kiro_trial_current":      nil,
		"kiro_trial_limit":        nil,
		"kiro_trial_percent":      nil,
		"kiro_trial_status":       nil,
		"kiro_trial_expiry":       nil,
	}
	if ku.NextResetDate != "" {
		updates["kiro_next_reset"] = ku.NextResetDate
	}
	if ku.SubscriptionTitle != "" {
		updates["kiro_subscription_title"] = ku.SubscriptionTitle
	}
	if ku.Trial != nil {
		updates["kiro_trial_current"] = ku.Trial.Current
		updates["kiro_trial_limit"] = ku.Trial.Limit
		updates["kiro_trial_percent"] = ku.Trial.Percent
		if ku.Trial.Status != "" {
			updates["kiro_trial_status"] = ku.Trial.Status
		}
		if ku.Trial.ExpiresAt != nil {
			updates["kiro_trial_expiry"] = ku.Trial.ExpiresAt.Unix()
		}
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, updates); err != nil {
		slog.Warn("sync_kiro_active_to_passive_failed", "account_id", accountID, "error", err)
	}
}

// buildPassiveKiroUsage rebuilds the kiro snapshot purely from Account.Extra samples
// written by syncKiroActiveToPassive — no upstream call. Returns an empty
// (KiroUsage=nil) UsageInfo when the account was never actively probed, so the cell
// renders "-" rather than a zero budget. Dual to buildPassiveOpenAIUsage.
func (s *AccountUsageService) buildPassiveKiroUsage(account *Account) *UsageInfo {
	now := time.Now()
	usage := &UsageInfo{Source: "passive", UpdatedAt: &now}
	if account == nil || account.Extra == nil {
		return usage
	}
	extra := account.Extra

	limit := parseExtraFloat64(extra["kiro_usage_limit"])
	current := parseExtraFloat64(extra["kiro_usage_current"])
	percent := parseExtraFloat64(extra["kiro_usage_percent"])
	nextReset, _ := extra["kiro_next_reset"].(string)
	subTitle, _ := extra["kiro_subscription_title"].(string)

	trial := buildPassiveKiroTrial(extra)

	if limit <= 0 && current <= 0 && percent <= 0 && nextReset == "" && subTitle == "" && trial == nil {
		return usage
	}

	usage.KiroUsage = &KiroUsageInfo{
		Current:           current,
		Limit:             limit,
		Percent:           percent,
		NextResetDate:     nextReset,
		SubscriptionTitle: subTitle,
		Trial:             trial,
	}
	if sampledAt := parseExtraSampledAt(extra["kiro_usage_sampled_at"]); sampledAt != nil {
		usage.UpdatedAt = sampledAt
	}
	return usage
}

// buildPassiveKiroTrial reconstructs the optional trial allowance from Extra; nil
// when no trial keys were sampled.
func buildPassiveKiroTrial(extra map[string]any) *KiroTrialInfo {
	limit := parseExtraFloat64(extra["kiro_trial_limit"])
	current := parseExtraFloat64(extra["kiro_trial_current"])
	percent := parseExtraFloat64(extra["kiro_trial_percent"])
	status, _ := extra["kiro_trial_status"].(string)
	expiryRaw := parseExtraFloat64(extra["kiro_trial_expiry"])

	if limit <= 0 && current <= 0 && percent <= 0 && status == "" && expiryRaw <= 0 {
		return nil
	}
	trial := &KiroTrialInfo{
		Current: current,
		Limit:   limit,
		Percent: percent,
		Status:  status,
	}
	if expiryRaw > 0 {
		t := time.Unix(int64(expiryRaw), 0)
		trial.ExpiresAt = &t
	}
	return trial
}
