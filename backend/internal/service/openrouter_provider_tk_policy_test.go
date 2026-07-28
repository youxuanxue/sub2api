package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

func TestIsPublicGroupForbiddenAggregatorAccount(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		channelType int
		credentials map[string]any
		want        bool
	}{
		{
			name:        "openrouter channel",
			platform:    PlatformNewAPI,
			channelType: newapiconstant.ChannelTypeOpenRouter,
			want:        true,
		},
		{
			name:        "coze channel",
			platform:    PlatformNewAPI,
			channelType: newapiconstant.ChannelTypeCoze,
			want:        true,
		},
		{
			name:        "ali channel",
			platform:    PlatformNewAPI,
			channelType: newapiconstant.ChannelTypeAli,
			want:        false,
		},
		{
			name:        "openrouter base url",
			platform:    PlatformNewAPI,
			channelType: 0,
			credentials: map[string]any{"base_url": "https://openrouter.ai/api/v1"},
			want:        true,
		},
		{
			name:        "anthropic native",
			platform:    PlatformAnthropic,
			channelType: 0,
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPublicGroupForbiddenAggregatorAccount(tt.platform, tt.channelType, tt.credentials)
			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestPublicGroupForbiddenAggregatorChannelTypes(t *testing.T) {
	got := PublicGroupForbiddenAggregatorChannelTypes()
	want := map[int]struct{}{
		newapiconstant.ChannelTypeOpenRouter: {},
		newapiconstant.ChannelTypeCoze:       {},
		newapiconstant.ChannelTypeSubmodel:   {},
	}
	if len(got) != len(want) {
		t.Fatalf("channel types = %v", got)
	}
	for _, ct := range got {
		if _, ok := want[ct]; !ok {
			t.Fatalf("unexpected channel type %d", ct)
		}
	}
}

func TestPublicGroupAggregatorChannelError_Error(t *testing.T) {
	err := &PublicGroupAggregatorChannelError{
		GroupID:      10,
		GroupName:    "public-newapi",
		AccountName:  "or-upstream",
		ChannelLabel: "OpenRouter",
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("expected message")
	}
}
