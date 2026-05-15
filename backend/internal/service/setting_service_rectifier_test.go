//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSettingService_GetRectifierSettings_DefaultsAPIKeySignatureEnabled(t *testing.T) {
	settingSvc := NewSettingService(&anthropicPassthroughSettingRepoStub{values: map[string]string{}}, &config.Config{})

	settings, err := settingSvc.GetRectifierSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.APIKeySignatureEnabled)
}

func TestSettingService_GetRectifierSettings_LegacyJSONDefaultsAPIKeySignatureEnabled(t *testing.T) {
	settingSvc := NewSettingService(&anthropicPassthroughSettingRepoStub{values: map[string]string{
		SettingKeyRectifierSettings: `{"enabled":true,"thinking_signature_enabled":true,"thinking_budget_enabled":true}`,
	}}, &config.Config{})

	settings, err := settingSvc.GetRectifierSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.APIKeySignatureEnabled)
}
