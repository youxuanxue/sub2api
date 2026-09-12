//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRegistrationOfferPublicPolicyAndInjection(t *testing.T) {
	cases := []struct {
		name         string
		values       map[string]string
		state, bonus string
	}{
		{"open", map[string]string{}, "open", "2.5"},
		{"closed", map[string]string{SettingKeyRegistrationEnabled: "false"}, "closed", ""},
		{"backend", map[string]string{SettingKeyBackendModeEnabled: "true"}, "closed", ""},
		{"invitation", map[string]string{SettingKeyInvitationCodeEnabled: "true"}, "invitation_required", ""},
		{"invalid policy", map[string]string{SettingKeyRegistrationEnabled: "broken"}, "unavailable", ""},
		{"missing policy", map[string]string{SettingKeyRegistrationEnabled: ""}, "unavailable", ""},
		{"email unconfigured", map[string]string{SettingKeyEmailVerifyEnabled: "true"}, "unavailable", ""},
		{"email configured", map[string]string{SettingKeyEmailVerifyEnabled: "true", SettingKeySMTPHost: "smtp.example.test"}, "open", "2.5"},
		{"captcha unconfigured", map[string]string{SettingKeyTurnstileEnabled: "true"}, "unavailable", ""},
		{"captcha configured", map[string]string{SettingKeyTurnstileEnabled: "true", SettingKeyTurnstileSiteKey: "public", SettingKeyTurnstileSecretKey: "private-fixture"}, "open", "2.5"},
		{"tencent invalid app id", map[string]string{SettingKeyTencentCaptchaEnabled: "true", SettingKeyTencentCaptchaAppID: "invalid", SettingKeyTencentCaptchaAppSecretKey: "private-fixture", SettingKeyTencentCaptchaCloudSecretID: "private-fixture", SettingKeyTencentCaptchaCloudSecretKey: "private-fixture"}, "unavailable", ""},
		{"tencent configured", map[string]string{SettingKeyTencentCaptchaEnabled: "true", SettingKeyTencentCaptchaAppID: "1234", SettingKeyTencentCaptchaAppSecretKey: "private-fixture", SettingKeyTencentCaptchaCloudSecretID: "private-fixture", SettingKeyTencentCaptchaCloudSecretKey: "private-fixture"}, "open", "2.5"},
		{"aliyun configured", map[string]string{SettingKeyAliyunCaptchaEnabled: "true", SettingKeyAliyunCaptchaSceneID: "scene", SettingKeyAliyunCaptchaPrefix: "prefix", SettingKeyAliyunCaptchaAccessKeyID: "private-fixture", SettingKeyAliyunCaptchaAccessKeySecret: "private-fixture"}, "open", "2.5"},
		{"aliyun missing key", map[string]string{SettingKeyAliyunCaptchaEnabled: "true", SettingKeyAliyunCaptchaSceneID: "scene", SettingKeyAliyunCaptchaPrefix: "prefix"}, "unavailable", ""},
		{"captcha conflict", map[string]string{SettingKeyTurnstileEnabled: "true", SettingKeyTencentCaptchaEnabled: "true"}, "unavailable", ""},
		{"invalid bonus", map[string]string{SettingKeySignupBonusBalance: "NaN"}, "open", ""},
		{"missing bonus", map[string]string{SettingKeySignupBonusBalance: ""}, "open", ""},
		{"disabled bonus", map[string]string{SettingKeySignupBonusEnabled: "false"}, "open", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := map[string]string{SettingKeyRegistrationEnabled: "true", SettingKeySignupBonusEnabled: "true", SettingKeySignupBonusBalance: "2.50"}
			for key, value := range tc.values {
				values[key] = value
			}
			svc := NewSettingService(&settingPublicRepoStub{values: values}, &config.Config{})
			public, err := svc.GetPublicSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, RegistrationOffer{State: tc.state, SignupBonusUSD: tc.bonus}, public.RegistrationOffer)
			payload, err := svc.GetPublicSettingsForInjection(context.Background())
			require.NoError(t, err)
			require.Equal(t, public.RegistrationOffer, payload.(*PublicSettingsInjectionPayload).RegistrationOffer)
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-fixture")
			require.NotContains(t, string(raw), "smtp.example.test")
		})
	}
}

func TestRegistrationOfferReadFailureAndRequiredCaptcha(t *testing.T) {
	svc := NewSettingService(&settingPublicRepoStub{err: errors.New("offline")}, &config.Config{})
	public, err := svc.GetPublicSettings(context.Background())
	require.Error(t, err)
	require.Nil(t, public)
	values := map[string]string{SettingKeyRegistrationEnabled: "true"}
	require.Equal(t, "unavailable", registrationOfferFromSettings(values, &PublicSettings{}, true).State)
}
