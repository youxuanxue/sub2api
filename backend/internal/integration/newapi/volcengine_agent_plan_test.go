//go:build unit

package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsVolcEngineAgentPlanBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		channelType int
		base        string
		want        bool
	}{
		{newapiconstant.ChannelTypeVolcEngine, VolcEngineAgentPlanBaseKey, true},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/plan/v3", true},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com", false},
		{newapiconstant.ChannelTypeDeepSeek, VolcEngineAgentPlanBaseKey, false},
	}
	for _, tc := range cases {
		got := IsVolcEngineAgentPlanBaseURL(tc.channelType, tc.base)
		if got != tc.want {
			t.Fatalf("IsVolcEngineAgentPlanBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
		}
	}
}
