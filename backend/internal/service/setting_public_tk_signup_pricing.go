package service

import "strconv"

// TokenKey: public signup-bonus + pricing-catalog preview fields for
// GetPublicSettings / HTML injection. Isolated so setting_public.go stays
// upstream-shaped (keys append + thin apply calls only).
//
// Struct field declarations on PublicSettings / PublicSettingsInjectionPayload
// remain in the upstream-shaped files — moving json-tagged fields would change
// the public settings API surface layout without reducing merge conflict.

func tkPublicSignupPricingSettingKeys() []string {
	return []string{
		SettingKeySignupBonusEnabled,
		SettingKeySignupBonusBalance,
		SettingKeyPricingCatalogPublic,
	}
}

type tkPublicSignupPricing struct {
	SignupBonusEnabled           bool
	SignupBonusBalanceDisplayUSD float64
	PricingCatalogPublic         bool
}

func tkParsePublicSignupPricing(settings map[string]string) tkPublicSignupPricing {
	signupBonusEnabled := !isFalseSettingValue(settings[SettingKeySignupBonusEnabled])
	signupBonusBalance := defaultSignupBonusBalanceUSD
	if v, err := strconv.ParseFloat(settings[SettingKeySignupBonusBalance], 64); err == nil && v >= 0 {
		signupBonusBalance = v
	}
	if !signupBonusEnabled {
		signupBonusBalance = 0
	}
	return tkPublicSignupPricing{
		SignupBonusEnabled:           signupBonusEnabled,
		SignupBonusBalanceDisplayUSD: signupBonusBalance,
		PricingCatalogPublic:         !isFalseSettingValue(settings[SettingKeyPricingCatalogPublic]),
	}
}

func tkApplyPublicSignupPricing(dst *PublicSettings, src tkPublicSignupPricing) {
	if dst == nil {
		return
	}
	dst.SignupBonusEnabled = src.SignupBonusEnabled
	dst.SignupBonusBalanceDisplayUSD = src.SignupBonusBalanceDisplayUSD
	dst.PricingCatalogPublic = src.PricingCatalogPublic
}

func tkApplyPublicSignupPricingInjection(dst *PublicSettingsInjectionPayload, src *PublicSettings) {
	if dst == nil || src == nil {
		return
	}
	dst.PricingCatalogPublic = src.PricingCatalogPublic
	dst.SignupBonusEnabled = src.SignupBonusEnabled
	dst.SignupBonusBalanceUSD = src.SignupBonusBalanceDisplayUSD
}
