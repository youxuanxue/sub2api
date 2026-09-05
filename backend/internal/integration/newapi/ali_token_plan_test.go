package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsAliTokenPlanBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		channelType int
		base        string
		want        bool
	}{
		{newapiconstant.ChannelTypeAli, AliTokenPlanBaseURL, true},
		{newapiconstant.ChannelTypeAli, AliTokenPlanBaseURL + "/", true},
		{newapiconstant.ChannelTypeAli, AliTokenPlanBaseURL + "/compatible-mode/v1", true},
		{newapiconstant.ChannelTypeAli, AliTokenPlanBaseURL + "/apps/anthropic", true},
		{newapiconstant.ChannelTypeAli, "https://dashscope.aliyuncs.com", false},
		{newapiconstant.ChannelTypeBaiduV2, AliTokenPlanBaseURL, false},
	}
	for _, tc := range cases {
		got := IsAliTokenPlanBaseURL(tc.channelType, tc.base)
		if got != tc.want {
			t.Fatalf("IsAliTokenPlanBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
		}
	}
}
