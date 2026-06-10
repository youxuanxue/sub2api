package service

import "context"

// IsAnthropicCanonicalIngressStrictEnabled reports whether the canonical
// Anthropic OAuth ingress should enforce the strict allow-list UA gate, the
// haiku mimicry completion, and the count_tokens UA gate. It defaults to false:
// without an explicit opt-in the canonical path keeps its current deny-list /
// upstream behavior (zero regression). When enabled, the hardening described in
// SettingKeyAnthropicCanonicalIngressStrictEnabled takes effect, scoped to
// anthropic OAuth accounts bound to the canonical TLS profile only.
//
// Reads through SettingService.GetValue so the value participates in the
// settings pubsub hot-update path (admin can toggle without a deploy).
func (s *SettingService) IsAnthropicCanonicalIngressStrictEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyAnthropicCanonicalIngressStrictEnabled)
	if err != nil {
		return false // 默认关闭，保持 deny-list / upstream 行为
	}
	return value == "true"
}
