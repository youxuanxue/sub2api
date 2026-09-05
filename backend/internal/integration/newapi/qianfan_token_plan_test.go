package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsQianfanTokenPlanBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		channelType int
		base        string
		want        bool
	}{
		{newapiconstant.ChannelTypeBaiduV2, QianfanTokenPlanBaseURL, true},
		{newapiconstant.ChannelTypeBaiduV2, QianfanTokenPlanBaseURL + "/", true},
		{newapiconstant.ChannelTypeBaiduV2, QianfanTokenPlanBaseURL + "/chat/completions", true},
		{newapiconstant.ChannelTypeBaiduV2, QianfanBaseURL, false},
		{newapiconstant.ChannelTypeBaiduV2, QianfanBaseURL + "/v2", false},
		{newapiconstant.ChannelTypeAli, QianfanTokenPlanBaseURL, false},
	}
	for _, tc := range cases {
		got := IsQianfanTokenPlanBaseURL(tc.channelType, tc.base)
		if got != tc.want {
			t.Fatalf("IsQianfanTokenPlanBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
		}
	}
}

func TestNormalizeQianfanTokenPlanBaseURL(t *testing.T) {
	t.Parallel()
	if got := NormalizeQianfanTokenPlanBaseURL(QianfanTokenPlanBaseURL + "/chat/completions/"); got != QianfanTokenPlanBaseURL {
		t.Fatalf("normalize = %q", got)
	}
	if got := NormalizeQianfanTokenPlanBaseURL(QianfanBaseURL); got != QianfanBaseURL {
		t.Fatalf("passthrough = %q", got)
	}
}
