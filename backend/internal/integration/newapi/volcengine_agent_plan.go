package newapi

import (
	newapiconstant "github.com/QuantumNous/new-api/constant"
)

const (
	// VolcEngineAgentPlanBaseKey is kept for backwards compatibility with
	// accounts created while the short-lived new-api fork exposed a special-base
	// lookup key. New accounts should store VolcEngineAgentPlanBaseURL instead.
	VolcEngineAgentPlanBaseKey = "doubao-agent-plan"
	// VolcEngineAgentPlanBaseURL is the OpenAI-compatible Agent Plan API root.
	// Native TokenKey forwarding appends /chat/completions or /responses to it.
	VolcEngineAgentPlanBaseURL = "https://ark.cn-beijing.volces.com/api/plan/v3"
	// VolcEngineCodingPlanBaseKey mirrors new-api's ChannelSpecialBases key for
	// Ark Coding Plan accounts.
	VolcEngineCodingPlanBaseKey = "doubao-coding-plan"
	// VolcEngineAgentPlanDefaultTestModel is the OpenAI-compatible chat probe
	// model documented for Agent Plan console exports.
	VolcEngineAgentPlanDefaultTestModel = "ark-code-latest"
)

// IsVolcEngineAgentPlanBaseURL reports whether base resolves to the Agent Plan
// root (/api/plan/v3) rather than pay-as-you-go /api/v3/*.
func IsVolcEngineAgentPlanBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeVolcEngine {
		return false
	}
	return NormalizeArkChannelBaseURL(channelType, base) == VolcEngineAgentPlanBaseURL
}
