package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

const (
	// QianfanTokenPlanBaseURL is the OpenAI-compatible Token Plan Person API root.
	// Native TokenKey forwarding appends /chat/completions to it.
	QianfanTokenPlanBaseURL = "https://qianfan.baidubce.com/v2/tokenplan/personal"
	// QianfanTokenPlanAnthropicBaseURL is the Anthropic-compatible Token Plan Person root.
	// Native forwarding appends /v1/messages to it.
	QianfanTokenPlanAnthropicBaseURL = "https://qianfan.baidubce.com/anthropic/tokenplan/personal"
	// QianfanTokenPlanDefaultTestModel is the current Token Plan Person Flash
	// identity used by the admin connectivity probe. deepseek-v4-flash is no
	// longer a plan mapping key.
	QianfanTokenPlanDefaultTestModel = "deepseek-v4.1-flash"
)

// NormalizeQianfanTokenPlanBaseURL collapses accepted Token Plan Person base
// spellings to the canonical OpenAI-compatible root. Returns the trimmed input
// unchanged when it is not a Token Plan Person host/path.
func NormalizeQianfanTokenPlanBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return base
	}
	lower := strings.ToLower(base)
	switch {
	case lower == QianfanTokenPlanBaseURL,
		strings.HasPrefix(lower, QianfanTokenPlanBaseURL+"/"):
		return QianfanTokenPlanBaseURL
	default:
		return base
	}
}

// IsQianfanTokenPlanBaseURL reports whether channelType/base resolve to the
// Baidu Qianfan Token Plan Person OpenAI-compatible root (not pay-as-you-go /v2).
func IsQianfanTokenPlanBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeBaiduV2 {
		return false
	}
	return NormalizeQianfanTokenPlanBaseURL(base) == QianfanTokenPlanBaseURL
}
