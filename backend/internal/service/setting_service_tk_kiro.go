package service

import "context"

// IsKiroEnabled reports whether the Kiro (sixth platform) forwarding path is
// enabled. This is the second of two ToS gates (the first is
// tkValidateKiroAccountCreate at account-creation time). It defaults to false:
// an operator must explicitly opt in after acknowledging the Kiro Terms of
// Service before any Kiro traffic is forwarded.
func (s *SettingService) IsKiroEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyKiroEnabled)
	if err != nil {
		return false // 默认关闭
	}
	return value == "true"
}
