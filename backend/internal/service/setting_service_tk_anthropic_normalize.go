package service

import (
	"context"
	"strconv"
)

// TokenKey: Anthropic native request body normalization toggle.
//
// Isolated from setting_service.go so upstream merges stay small. Mirrors the
// shape of setting_service_tk_bridge_passkey_payments.go (tkAppend / tkMerge /
// tkApply triplet + IsXxxEnabled helper).

func (s *SettingService) tkAppendAnthropicNormalizeSettingUpdates(updates map[string]string, settings *SystemSettings) {
	updates[SettingKeyAnthropicRequestNormalizeEnabled] = strconv.FormatBool(settings.AnthropicRequestNormalizeEnabled)
}

func tkMergeDefaultAnthropicNormalizeSettings(defaults map[string]string) {
	defaults[SettingKeyAnthropicRequestNormalizeEnabled] = "true"
}

func tkApplyAnthropicNormalizeParsed(settings map[string]string, result *SystemSettings) {
	// Default = true; treat missing/empty as enabled so a fresh install with no
	// migration still normalizes. Explicit "false" is the only way to disable.
	result.AnthropicRequestNormalizeEnabled = !isFalseSettingValue(settings[SettingKeyAnthropicRequestNormalizeEnabled])
}

// IsAnthropicRequestNormalizeEnabled reports whether the gateway should rewrite
// known-bad Anthropic native request shapes (tool_choice string -> object;
// strip thinking when tool_choice forces tool use). Defaults to true: an
// unreadable repo or empty value means enabled.
//
// Reads through the shared 60s gatewayForwardingCache (singleflight) — every
// adjacent setting (fingerprint unification, metadata passthrough, cch
// signing, anthropic cache TTL 1h injection, message cache_control rewrite)
// uses the same cache for the hot Anthropic forward path. Direct DB lookup
// here would add one DB roundtrip per /v1/messages request.
func (s *SettingService) IsAnthropicRequestNormalizeEnabled(ctx context.Context) bool {
	if s == nil {
		return true
	}
	return s.getGatewayForwardingSettingsCached(ctx).anthropicRequestNormalize
}
