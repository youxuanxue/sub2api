package newapi

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsQianfanBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		channelType int
		base        string
		want        bool
	}{
		{newapiconstant.ChannelTypeBaiduV2, QianfanBaseURL, true},
		{newapiconstant.ChannelTypeBaiduV2, QianfanBaseURL + "/", true},
		{newapiconstant.ChannelTypeBaiduV2, QianfanBaseURL + "/v2", false},
		{newapiconstant.ChannelTypeDeepSeek, QianfanBaseURL, false},
		{newapiconstant.ChannelTypeVolcEngine, QianfanBaseURL, false},
	}
	for _, tc := range cases {
		got := IsQianfanBaseURL(tc.channelType, tc.base)
		if got != tc.want {
			t.Fatalf("IsQianfanBaseURL(%d, %q) = %v, want %v", tc.channelType, tc.base, got, tc.want)
		}
	}
}
