package service

import (
	"math"
	"strconv"
	"strings"
)

// RegistrationOffer is the public email-registration policy, not a reservation
// or a guarantee of trial-key provisioning or upstream capacity.
type RegistrationOffer struct {
	State          string `json:"state"`
	SignupBonusUSD string `json:"signup_bonus_usd,omitempty"`
}

func registrationOfferSettingKeys() []string {
	return []string{SettingKeySMTPHost, SettingKeyTurnstileSecretKey,
		SettingKeyTencentCaptchaAppSecretKey, SettingKeyTencentCaptchaCloudSecretID,
		SettingKeyTencentCaptchaCloudSecretKey, SettingKeyAliyunCaptchaAccessKeyID,
		SettingKeyAliyunCaptchaAccessKeySecret}
}

func registrationOfferFromSettings(values map[string]string, public *PublicSettings, captchaRequired bool) RegistrationOffer {
	offer := RegistrationOffer{State: "unavailable"}
	if public.BackendModeEnabled || values[SettingKeyRegistrationEnabled] == "false" {
		offer.State = "closed"
		return offer
	}
	if values[SettingKeyRegistrationEnabled] != "true" {
		return offer
	}
	// Unknown boolean values must not become an affirmative public promise.
	for _, key := range []string{SettingKeyBackendModeEnabled, SettingKeyInvitationCodeEnabled,
		SettingKeyEmailVerifyEnabled, SettingKeyTurnstileEnabled, SettingKeyTencentCaptchaEnabled,
		SettingKeyAliyunCaptchaEnabled} {
		if v := values[key]; v != "" && v != "true" && v != "false" {
			return offer
		}
	}
	if public.EmailVerifyEnabled && strings.TrimSpace(values[SettingKeySMTPHost]) == "" {
		return offer
	}
	if captchaProvidersConflict(public.TurnstileEnabled, public.TencentCaptchaEnabled, public.AliyunCaptchaEnabled) {
		return offer
	}
	config := captchaProviderConfigFromSettings(values)
	switch {
	case public.TurnstileEnabled:
		if strings.TrimSpace(values[SettingKeyTurnstileSiteKey]) == "" || strings.TrimSpace(config.TurnstileSecretKey) == "" {
			return offer
		}
	case public.TencentCaptchaEnabled:
		if _, ok := parseTencentCaptchaCredentials(config.Tencent); !ok {
			return offer
		}
	case public.AliyunCaptchaEnabled:
		if _, ok := aliyunCaptchaCredentials(config.Aliyun); !ok || strings.TrimSpace(values[SettingKeyAliyunCaptchaPrefix]) == "" {
			return offer
		}
	default:
		if captchaRequired {
			return offer
		}
	}
	if public.InvitationCodeEnabled {
		offer.State = "invitation_required"
		return offer
	}
	offer.State = "open"
	// Reuse the existing signup amount projection, but never advertise a
	// fallback amount inferred from missing/corrupt configuration.
	amount, err := strconv.ParseFloat(values[SettingKeySignupBonusBalance], 64)
	if values[SettingKeySignupBonusEnabled] == "true" && err == nil && amount > 0 && !math.IsNaN(amount) && !math.IsInf(amount, 0) {
		offer.SignupBonusUSD = strconv.FormatFloat(public.SignupBonusBalanceDisplayUSD, 'f', -1, 64)
	}
	return offer
}
