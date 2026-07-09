package service

// TokenKey-owned admin/settings defaults kept outside setting_parse.go so
// upstream default changes only need a thin call-site rebase.
const (
	tkDefaultSiteName                  = "TokenKey"
	tkDefaultSiteSubtitle              = "AI API Gateway Platform"
	tkDefaultFallbackModelAnthropic    = "claude-sonnet-4-6"
	tkDefaultFallbackModelOpenAI       = "gpt-5.4"
	tkDefaultFallbackModelGemini       = "gemini-2.5-pro"
	tkDefaultFallbackModelAntigravity  = "gemini-3-flash"
	tkDefaultStickyRoutingEnabledValue = "true"
)

func tkMergeDefaultBrandGatewaySettings(defaults map[string]string) {
	defaults[SettingKeySiteName] = tkDefaultSiteName
	defaults[SettingKeySiteSubtitle] = tkDefaultSiteSubtitle
	defaults[SettingKeyFallbackModelAnthropic] = tkDefaultFallbackModelAnthropic
	defaults[SettingKeyFallbackModelOpenAI] = tkDefaultFallbackModelOpenAI
	defaults[SettingKeyFallbackModelGemini] = tkDefaultFallbackModelGemini
	defaults[SettingKeyFallbackModelAntigravity] = tkDefaultFallbackModelAntigravity
	defaults[SettingKeyStickyRoutingEnabled] = tkDefaultStickyRoutingEnabledValue
}

func tkApplyBrandGatewayParsed(settings map[string]string, result *SystemSettings) {
	if result == nil {
		return
	}
	result.SiteName = tkStringSettingOrDefault(settings, SettingKeySiteName, result.SiteName, tkDefaultSiteName)
	result.SiteSubtitle = tkStringSettingOrDefault(settings, SettingKeySiteSubtitle, result.SiteSubtitle, tkDefaultSiteSubtitle)
	result.FallbackModelAnthropic = tkStringSettingOrDefault(settings, SettingKeyFallbackModelAnthropic, result.FallbackModelAnthropic, tkDefaultFallbackModelAnthropic)
	result.FallbackModelOpenAI = tkStringSettingOrDefault(settings, SettingKeyFallbackModelOpenAI, result.FallbackModelOpenAI, tkDefaultFallbackModelOpenAI)
	result.FallbackModelGemini = tkStringSettingOrDefault(settings, SettingKeyFallbackModelGemini, result.FallbackModelGemini, tkDefaultFallbackModelGemini)
	result.FallbackModelAntigravity = tkStringSettingOrDefault(settings, SettingKeyFallbackModelAntigravity, result.FallbackModelAntigravity, tkDefaultFallbackModelAntigravity)
	result.StickyRoutingEnabled = !isFalseSettingValue(settings[SettingKeyStickyRoutingEnabled])
}

func tkStringSettingOrDefault(settings map[string]string, key, current, fallback string) string {
	if value := settings[key]; value != "" {
		return value
	}
	if fallback != "" {
		return fallback
	}
	return current
}
