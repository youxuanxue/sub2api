package service

import (
	"encoding/json"
)

// tkMergeDefaultPanelRateLimitSettings keeps upstream panel rate limiting opt-in on
// fresh installs until an operator enables it in admin settings.
func tkMergeDefaultPanelRateLimitSettings(defaults map[string]string) {
	s := DefaultPanelRateLimitSettings()
	s.Enabled = false
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	defaults[SettingKeyPanelRateLimitSettings] = string(data)
}
