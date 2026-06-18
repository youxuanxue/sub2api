package service

import "context"

// GetRawSettingValue reads a single setting value by key, bypassing the typed
// SystemSettings accessors. It exists for TK runtime knobs stored as free-form
// keys (e.g. SettingKeyTKPricingOverlayRuntime) that are not fields on
// SystemSettings. Returns ok=false when the key is absent, errored, or set to an
// empty string (callers treat empty as "use the compile default / floor").
//
// TokenKey-only (CLAUDE.md §5): kept in a companion so setting_service.go stays
// close to upstream shape.
func (s *SettingService) GetRawSettingValue(ctx context.Context, key string) (string, bool) {
	if s == nil || s.settingRepo == nil {
		return "", false
	}
	v, err := s.settingRepo.GetValue(ctx, key)
	if err != nil {
		return "", false
	}
	return v, v != ""
}
