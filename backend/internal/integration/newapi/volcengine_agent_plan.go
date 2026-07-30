package newapi

import (
	newapiconstant "github.com/QuantumNous/new-api/constant"
)

const (
	// VolcEngineAgentPlanBaseKey is the ChannelSpecialBases lookup key for Ark
	// Agent Plan accounts. Admins may store this magic value or a full /api/plan
	// URL; NormalizeArkChannelBaseURL canonicalizes both shapes.
	VolcEngineAgentPlanBaseKey = "doubao-agent-plan"
	// VolcEngineCodingPlanBaseKey mirrors new-api's ChannelSpecialBases key for
	// Ark Coding Plan accounts.
	VolcEngineCodingPlanBaseKey = "doubao-coding-plan"
	// VolcEngineAgentPlanDefaultTestModel is the OpenAI-compatible chat probe
	// model documented for Agent Plan console exports.
	VolcEngineAgentPlanDefaultTestModel = "ark-code-latest"
)

// IsVolcEngineAgentPlanBaseURL reports whether base resolves to the Agent Plan
// adaptor path (/api/plan/v3/*) rather than pay-as-you-go /api/v3/*.
func IsVolcEngineAgentPlanBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeVolcEngine {
		return false
	}
	return NormalizeArkChannelBaseURL(channelType, base) == VolcEngineAgentPlanBaseKey
}
