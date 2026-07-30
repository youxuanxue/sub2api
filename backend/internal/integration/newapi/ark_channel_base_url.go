package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// NormalizeArkChannelBaseURL trims mistaken path suffixes from upstreams whose
// new-api adaptors append their own API path. Admins often paste the full chat
// endpoint or version root from docs; keeping that in base_url would produce a
// broken double path.
func NormalizeArkChannelBaseURL(channelType int, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return base
	}
	base = strings.TrimRight(base, "/")
	switch channelType {
	case newapiconstant.ChannelTypeVolcEngine, newapiconstant.ChannelTypeDoubaoVideo:
		if base == VolcEngineAgentPlanBaseKey || base == VolcEngineCodingPlanBaseKey {
			return base
		}
		for _, plan := range []struct {
			suffix string
			key    string
		}{
			{"/api/plan/v3/chat/completions", VolcEngineAgentPlanBaseKey},
			{"/api/plan/v3/responses", VolcEngineAgentPlanBaseKey},
			{"/api/plan/v3", VolcEngineAgentPlanBaseKey},
			{"/api/plan", VolcEngineAgentPlanBaseKey},
			{"/api/coding/v3/chat/completions", VolcEngineCodingPlanBaseKey},
			{"/api/coding/v3", VolcEngineCodingPlanBaseKey},
			{"/api/coding", VolcEngineCodingPlanBaseKey},
		} {
			if strings.HasSuffix(base, plan.suffix) {
				return plan.key
			}
		}
		for _, suf := range []string{
			"/api/v3/chat/completions",
			"/api/v3/bots/chat/completions",
			"/api/v3/models",
			"/api/v3",
		} {
			if strings.HasSuffix(base, suf) {
				return strings.TrimRight(strings.TrimSuffix(base, suf), "/")
			}
		}
	case newapiconstant.ChannelTypeZhipu_v4:
		for _, suf := range []string{
			"/api/paas/v4/chat/completions",
			"/api/paas/v4/models",
			"/api/paas/v4",
		} {
			if strings.HasSuffix(base, suf) {
				return strings.TrimRight(strings.TrimSuffix(base, suf), "/")
			}
		}
	}
	return base
}
