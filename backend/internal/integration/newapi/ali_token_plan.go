package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

const (
	// AliTokenPlanBaseURL is the bare Aliyun MaaS Token Plan host. The Ali
	// adaptor appends /compatible-mode/v1/* and /apps/anthropic/v1/messages.
	AliTokenPlanBaseURL = "https://token-plan.cn-beijing.maas.aliyuncs.com"
	// AliTokenPlanDefaultTestModel is a chat model listed by GET
	// {AliTokenPlanBaseURL}/compatible-mode/v1/models.
	AliTokenPlanDefaultTestModel = "qwen3.6-flash"
)

// NormalizeAliTokenPlanBaseURL collapses accepted Token Plan base spellings
// (bare host, /compatible-mode/v1, /apps/anthropic) to the canonical host root.
func NormalizeAliTokenPlanBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return base
	}
	lower := strings.ToLower(base)
	switch {
	case lower == AliTokenPlanBaseURL,
		strings.HasPrefix(lower, AliTokenPlanBaseURL+"/"):
		return AliTokenPlanBaseURL
	default:
		return base
	}
}

// IsAliTokenPlanBaseURL reports whether channelType/base resolve to the Aliyun
// MaaS Token Plan host rather than DashScope pay-as-you-go.
func IsAliTokenPlanBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeAli {
		return false
	}
	return NormalizeAliTokenPlanBaseURL(base) == AliTokenPlanBaseURL
}
