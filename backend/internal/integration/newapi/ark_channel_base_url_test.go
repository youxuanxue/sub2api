//go:build unit

package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestNormalizeArkChannelBaseURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		channelType int
		in          string
		want        string
	}{
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com", "https://ark.cn-beijing.volces.com"},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com"},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/v3/", "https://ark.cn-beijing.volces.com"},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/v3/chat/completions", "https://ark.cn-beijing.volces.com"},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/v3/models", "https://ark.cn-beijing.volces.com"},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/plan/v3", VolcEngineAgentPlanBaseURL},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/plan/v3/", VolcEngineAgentPlanBaseURL},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/plan/v3/chat/completions", VolcEngineAgentPlanBaseURL},
		{newapiconstant.ChannelTypeVolcEngine, VolcEngineAgentPlanBaseKey, VolcEngineAgentPlanBaseURL},
		{newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com/api/coding/v3", VolcEngineCodingPlanBaseKey},
		{newapiconstant.ChannelTypeVolcEngine, VolcEngineCodingPlanBaseKey, VolcEngineCodingPlanBaseKey},
		{newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com"},
		{1, "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v3"},
		{newapiconstant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn", "https://open.bigmodel.cn"},
		{newapiconstant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn"},
		{newapiconstant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn/api/paas/v4/", "https://open.bigmodel.cn"},
		{newapiconstant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn/api/paas/v4/chat/completions", "https://open.bigmodel.cn"},
		{newapiconstant.ChannelTypeZhipu_v4, "https://open.bigmodel.cn/api/paas/v4/models", "https://open.bigmodel.cn"},
		{newapiconstant.ChannelTypeZhipu, "https://open.bigmodel.cn/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4"},
	}
	for _, tt := range tests {
		got := NormalizeArkChannelBaseURL(tt.channelType, tt.in)
		if got != tt.want {
			t.Fatalf("NormalizeArkChannelBaseURL(%d, %q) = %q, want %q", tt.channelType, tt.in, got, tt.want)
		}
	}
}
