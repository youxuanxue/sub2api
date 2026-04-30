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
		{newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com"},
		{1, "https://ark.cn-beijing.volces.com/api/v3", "https://ark.cn-beijing.volces.com/api/v3"},
	}
	for _, tt := range tests {
		got := NormalizeArkChannelBaseURL(tt.channelType, tt.in)
		if got != tt.want {
			t.Fatalf("NormalizeArkChannelBaseURL(%d, %q) = %q, want %q", tt.channelType, tt.in, got, tt.want)
		}
	}
}
