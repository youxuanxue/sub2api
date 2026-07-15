package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

var kiroBonusKeySanitizer = regexp.MustCompile(`[^a-z0-9]+`)

var errKiroUsageTokenRefreshInProgress = errors.New("Kiro OAuth token refresh already in progress")

var kiroUsageSensitiveErrorKeys = []string{
	"accessToken",
	"refreshToken",
	"idToken",
	"clientId",
	"clientSecret",
	"deviceCode",
}

type kiroAccountInfoFetcher func(*kiroproto.Account) (*kiroproto.AccountInfo, error)

// KiroUsageInfo is the kiro (CodeWhisperer) credits/subscription snapshot surfaced
// in UsageInfo. Unlike anthropic/openai's rolling 5h/7d windows, kiro exposes a
// credits budget (Current/Limit) that resets on a monthly date, plus an optional
// free-trial allowance with its own expiry. Percent is 0-100 to match
// UsageProgress.Utilization so the edge DTO can render it as a window bar.
type KiroUsageInfo struct {
	Current           float64         `json:"current,omitempty"`
	Limit             float64         `json:"limit,omitempty"`
	Percent           float64         `json:"percent,omitempty"` // 0-100
	NextResetDate     string          `json:"next_reset_date,omitempty"`
	SubscriptionTitle string          `json:"subscription_title,omitempty"`
	Trial             *KiroTrialInfo  `json:"trial,omitempty"`
	Bonuses           []KiroBonusInfo `json:"bonuses,omitempty"`
}

// KiroBonusInfo is one promotional/bonus credits bucket from getUsageLimits.
type KiroBonusInfo struct {
	Code      string     `json:"code,omitempty"`
	Label     string     `json:"label,omitempty"`
	Current   float64    `json:"current,omitempty"`
	Limit     float64    `json:"limit,omitempty"`
	Percent   float64    `json:"percent,omitempty"` // 0-100
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
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
//   - force=true (the operator's explicit「查询」): calls RefreshAccountInfo
//     (GetUsageLimits) with the stored token. A 401/403 Invalid-token response gets
//     one lock-protected forced refresh and one retry. The successful snapshot is
//     written to passive Extra for later upstream-free reads.
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
		usageAccount := account
		kiroAcct := usageAccount.toKiroProtoAccount()
		info, err := s.fetchKiroAccountInfo(kiroAcct)
		if err != nil && isKiroUsageTokenAuthError(err) {
			refreshedAccount, didRefresh, refreshErr := s.refreshKiroUsageAccount(ctx, usageAccount)
			if refreshErr != nil {
				slog.Warn("kiro usage token refresh failed",
					"account_id", account.ID,
					"error", redactKiroUsageError(refreshErr))
				err = fmt.Errorf("%v; Kiro OAuth token refresh failed", err)
			}
			if didRefresh {
				usageAccount = refreshedAccount
				kiroAcct = usageAccount.toKiroProtoAccount()
				info, err = s.fetchKiroAccountInfo(kiroAcct)
			}
		}
		if err != nil {
			return s.degradedKiroUsage(passive, account, err), nil
		}

		s.persistKiroProfileArnIfChanged(ctx, usageAccount, kiroAcct)

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

func (s *AccountUsageService) fetchKiroAccountInfo(account *kiroproto.Account) (*kiroproto.AccountInfo, error) {
	if s != nil && s.kiroUsageFetcher != nil {
		return s.kiroUsageFetcher(account)
	}
	return kiroproto.RefreshAccountInfo(account)
}

// refreshKiroUsageAccount force-refreshes only credentials. It deliberately does not
// clear account errors or re-enable scheduling: a successful control-plane query
// is not proof that a prior 402 quota rejection has recovered.
func (s *AccountUsageService) refreshKiroUsageAccount(ctx context.Context, account *Account) (*Account, bool, error) {
	if s == nil || account == nil || s.oauthRefreshAPI == nil {
		return account, false, nil
	}
	executor := s.kiroOAuthRefreshExecutor
	if executor == nil {
		executor = NewKiroTokenRefresher()
	}
	if !executor.CanRefresh(account) {
		return account, false, nil
	}
	result, err := s.oauthRefreshAPI.RefreshNow(ctx, account, executor)
	if err != nil {
		return account, false, err
	}
	if result == nil {
		return account, false, fmt.Errorf("Kiro OAuth token refresh returned no result")
	}
	if result.LockHeld {
		return account, false, errKiroUsageTokenRefreshInProgress
	}
	if result.Account != nil {
		return result.Account, true, nil
	}
	return account, true, nil
}

func isKiroUsageTokenAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "http 401") {
		return true
	}
	if !strings.Contains(msg, "http 403") {
		return false
	}
	return strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "invalid bearer") ||
		(strings.Contains(msg, "bearer token") && strings.Contains(msg, "invalid")) ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "expired token")
}

func (s *AccountUsageService) degradedKiroUsage(passive *UsageInfo, account *Account, err error) *UsageInfo {
	safeError := redactKiroUsageError(err)
	slog.Warn("kiro usage fetch failed, returning degraded response", "account_id", account.ID, "error", safeError)
	passive.Error = "usage API error: " + safeError
	enrichUsageWithAccountError(passive, account)
	return passive
}

func redactKiroUsageError(err error) string {
	if err == nil {
		return ""
	}
	return logredact.RedactText(err.Error(), kiroUsageSensitiveErrorKeys...)
}

// persistKiroProfileArnIfChanged writes a freshly resolved profile_arn back to account
// credentials so subsequent usage/gateway calls do not repeat ListAvailableProfiles or
// keep sending a stale ARN that triggers HTTP 400 Invalid profileArn.
func (s *AccountUsageService) persistKiroProfileArnIfChanged(ctx context.Context, account *Account, kiroAcct *kiroproto.Account) {
	PersistKiroProfileArnIfChanged(ctx, s.accountRepo, account, kiroAcct)
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
		if info.TrialUsageLimit > 0 && trial.Percent == 0 && info.TrialUsageCurrent > 0 {
			trial.Percent = (info.TrialUsageCurrent / info.TrialUsageLimit) * 100
		}
		if info.TrialExpiresAt > 0 {
			t := time.Unix(info.TrialExpiresAt, 0)
			trial.ExpiresAt = &t
		}
		ku.Trial = trial
	}
	if len(info.Bonuses) > 0 {
		ku.Bonuses = make([]KiroBonusInfo, 0, len(info.Bonuses))
		for _, bonus := range info.Bonuses {
			ku.Bonuses = append(ku.Bonuses, mapKiroBonusFromProto(bonus))
		}
	}
	usage.KiroUsage = ku
	return usage
}

func mapKiroBonusFromProto(bonus kiroproto.KiroBonusInfo) KiroBonusInfo {
	out := KiroBonusInfo{
		Code:    strings.TrimSpace(bonus.Code),
		Label:   strings.TrimSpace(bonus.Label),
		Current: bonus.Current,
		Limit:   bonus.Limit,
		Percent: bonus.Percent,
		Status:  strings.TrimSpace(bonus.Status),
	}
	if out.Label == "" {
		out.Label = out.Code
	}
	if out.Limit > 0 && out.Percent == 0 && out.Current > 0 {
		out.Percent = (out.Current / out.Limit) * 100
	}
	if bonus.ExpiresAt > 0 {
		t := time.Unix(bonus.ExpiresAt, 0)
		out.ExpiresAt = &t
	}
	return out
}

func kiroBonusQuotaKey(code string) string {
	slug := strings.Trim(kiroBonusKeySanitizer.ReplaceAllString(strings.ToLower(strings.TrimSpace(code)), "_"), "_")
	if slug == "" {
		slug = "unknown"
	}
	return "kiro_bonus_" + slug
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
		"kiro_bonuses":            nil,
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
	if len(ku.Bonuses) > 0 {
		if raw, err := json.Marshal(ku.Bonuses); err == nil {
			updates["kiro_bonuses"] = string(raw)
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
	// A passive Kiro response has a meaningful timestamp only when an active
	// GetUsageLimits call has persisted a sample. Do not stamp an empty/degraded
	// response with the current time; that would make a missing snapshot look
	// freshly sampled in the edge overview.
	usage := &UsageInfo{Source: "passive"}
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
	bonuses := buildPassiveKiroBonuses(extra)

	if limit <= 0 && current <= 0 && percent <= 0 && nextReset == "" && subTitle == "" && trial == nil && len(bonuses) == 0 {
		return usage
	}

	usage.KiroUsage = &KiroUsageInfo{
		Current:           current,
		Limit:             limit,
		Percent:           percent,
		NextResetDate:     nextReset,
		SubscriptionTitle: subTitle,
		Trial:             trial,
		Bonuses:           bonuses,
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

func buildPassiveKiroBonuses(extra map[string]any) []KiroBonusInfo {
	raw, ok := extra["kiro_bonuses"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var bonuses []KiroBonusInfo
	if err := json.Unmarshal([]byte(raw), &bonuses); err != nil {
		return nil
	}
	if len(bonuses) == 0 {
		return nil
	}
	return bonuses
}
