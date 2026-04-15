package service

import (
	"context"
	"strconv"
)

// TokenKey: new-api bridge settings are isolated from setting_service.go to keep upstream merges small.

func (s *SettingService) tkAppendTokenKeyBridgeSettingUpdates(updates map[string]string, settings *SystemSettings) {
	updates[SettingKeyNewAPIBridgeEnabled] = strconv.FormatBool(settings.NewAPIBridgeEnabled)
}

func tkMergeDefaultTokenKeyBridgeSettings(defaults map[string]string) {
	defaults[SettingKeyNewAPIBridgeEnabled] = "true"
}

func tkApplyTokenKeyBridgeParsed(settings map[string]string, result *SystemSettings) {
	result.NewAPIBridgeEnabled = !isFalseSettingValue(settings[SettingKeyNewAPIBridgeEnabled])
}

func (s *SettingService) IsNewAPIBridgeEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return true
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyNewAPIBridgeEnabled)
	if err != nil {
		return true
	}
	return !isFalseSettingValue(value)
}
