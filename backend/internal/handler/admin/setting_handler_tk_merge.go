package admin

import "github.com/Wei-Shaw/sub2api/internal/service"

// tkCoalesceOptionalBool merges a JSON pointer field with the previous persisted value
// (nil pointer means "keep previous"), used for TokenKey-specific toggles on SystemSettings.
func tkCoalesceOptionalBool(req *bool, previous bool) bool {
	if req != nil {
		return *req
	}
	return previous
}

// tkApplyTokenKeySettingsFields merges TokenKey bridge toggles from the admin update payload.
func tkApplyTokenKeySettingsFields(dst *service.SystemSettings, req *UpdateSettingsRequest, previous *service.SystemSettings) {
	if dst == nil || req == nil || previous == nil {
		return
	}
	dst.NewAPIBridgeEnabled = tkCoalesceOptionalBool(req.NewAPIBridgeEnabled, previous.NewAPIBridgeEnabled)
}

// tkTokenKeyBridgeSetting exposes the bridge flag for admin settings DTO assembly.
func tkTokenKeyBridgeSetting(s *service.SystemSettings) bool {
	if s == nil {
		return false
	}
	return s.NewAPIBridgeEnabled
}
