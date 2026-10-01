package service

import (
	"context"
	"strconv"
	"strings"
)

// TokenKey: signup / trial antifraud settings (CallModel C-end open).
// Isolated from setting_service.go to keep upstream merges small.
//
// Wiring mirrors cold-start companions:
//   - tkMergeDefaultAntifraudSettings ← InitializeDefaultSettings
//   - tkApplyAntifraudParsed          ← parseSettings
//   - tkAppendAntifraudSettingUpdates ← UpdateSystemSettings

const (
	defaultSignupBonusIPDailyLimit       = 3
	defaultTrialUnpaidMediaMaxBalanceUSD = 2.00
)

func tkMergeDefaultAntifraudSettings(defaults map[string]string) {
	defaults[SettingKeySignupBonusIPDailyLimit] = strconv.Itoa(defaultSignupBonusIPDailyLimit)
	defaults[SettingKeyTrialUnpaidMediaBlocked] = "true"
	defaults[SettingKeyTrialUnpaidMediaMaxBalance] = strconv.FormatFloat(defaultTrialUnpaidMediaMaxBalanceUSD, 'f', 8, 64)
}

func tkApplyAntifraudParsed(settings map[string]string, result *SystemSettings) {
	result.SignupBonusIPDailyLimit = parseSignupBonusIPDailyLimit(settings[SettingKeySignupBonusIPDailyLimit])
	// Missing / corrupt rows fail-closed-toward-on so a fresh C-end open still
	// blocks unpaid media burn.
	result.TrialUnpaidMediaBlocked = !isFalseSettingValue(settings[SettingKeyTrialUnpaidMediaBlocked])
	result.TrialUnpaidMediaMaxBalance = parseTrialUnpaidMediaMaxBalance(settings[SettingKeyTrialUnpaidMediaMaxBalance])
}

func (s *SettingService) tkAppendAntifraudSettingUpdates(updates map[string]string, settings *SystemSettings) {
	if settings.SignupBonusIPDailyLimit < 0 {
		settings.SignupBonusIPDailyLimit = 0
	}
	updates[SettingKeySignupBonusIPDailyLimit] = strconv.Itoa(settings.SignupBonusIPDailyLimit)
	updates[SettingKeyTrialUnpaidMediaBlocked] = strconv.FormatBool(settings.TrialUnpaidMediaBlocked)
	if settings.TrialUnpaidMediaMaxBalance < 0 {
		settings.TrialUnpaidMediaMaxBalance = 0
	}
	updates[SettingKeyTrialUnpaidMediaMaxBalance] = strconv.FormatFloat(settings.TrialUnpaidMediaMaxBalance, 'f', 8, 64)
}

func parseSignupBonusIPDailyLimit(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSignupBonusIPDailyLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return defaultSignupBonusIPDailyLimit
	}
	return v
}

func parseTrialUnpaidMediaMaxBalance(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultTrialUnpaidMediaMaxBalanceUSD
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return defaultTrialUnpaidMediaMaxBalanceUSD
	}
	return v
}

// GetSignupBonusIPDailyLimit returns the per-IP daily bonus grant cap.
// 0 means unlimited. Missing / corrupt rows fall back to the default (3).
func (s *SettingService) GetSignupBonusIPDailyLimit(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return defaultSignupBonusIPDailyLimit
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeySignupBonusIPDailyLimit)
	if err != nil {
		return defaultSignupBonusIPDailyLimit
	}
	return parseSignupBonusIPDailyLimit(value)
}

// IsTrialUnpaidMediaBlocked returns true unless the admin explicitly stored "false".
// Missing rows default ON so unpaid trial burn is gated on a fresh deploy.
func (s *SettingService) IsTrialUnpaidMediaBlocked(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTrialUnpaidMediaBlocked)
	if err != nil {
		return true
	}
	return !isFalseSettingValue(value)
}

// GetTrialUnpaidMediaMaxBalance returns the balance ceiling (USD) used with
// TotalRecharged<=0 to identify trial-shaped wallets. Missing rows → 2.00.
func (s *SettingService) GetTrialUnpaidMediaMaxBalance(ctx context.Context) float64 {
	if s == nil || s.settingRepo == nil {
		return defaultTrialUnpaidMediaMaxBalanceUSD
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTrialUnpaidMediaMaxBalance)
	if err != nil {
		return defaultTrialUnpaidMediaMaxBalanceUSD
	}
	return parseTrialUnpaidMediaMaxBalance(value)
}
