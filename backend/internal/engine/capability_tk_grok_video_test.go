//go:build unit

package engine

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// TestIsVideoSupportedForAccount pins the platform-aware video gate: grok is
// eligible at channel_type=0 (native xAI OAuth, no task adaptor), while every
// other platform still falls back to the channel_type-derived predicate
// (IsVideoSupportedChannelType). This guards the deliberate grok exception from
// drifting back into the channel_type-only gate, which would re-block grok video.
func TestIsVideoSupportedForAccount(t *testing.T) {
	cases := []struct {
		name        string
		platform    string
		channelType int
		want        bool
	}{
		// grok: always eligible regardless of channel_type (native arm, ct=0).
		{"grok_ct0", domain.PlatformGrok, 0, true},
		{"grok_ct_negative", domain.PlatformGrok, -1, true},
		{"grok_ct_positive", domain.PlatformGrok, 45, true},
		// non-grok: defers to the channel_type-derived registry predicate.
		{"openai_ct0_unsupported", domain.PlatformOpenAI, 0, false},
		{"anthropic_ct0_unsupported", domain.PlatformAnthropic, 0, false},
		{"newapi_volcengine_supported", domain.PlatformNewAPI, 45, true},
		{"newapi_doubao_video_supported", domain.PlatformNewAPI, 54, true},
		{"newapi_unknown_unsupported", domain.PlatformNewAPI, 9999, false},
		{"empty_platform_ct0", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsVideoSupportedForAccount(tc.platform, tc.channelType); got != tc.want {
				t.Fatalf("IsVideoSupportedForAccount(%q, %d) = %v, want %v", tc.platform, tc.channelType, got, tc.want)
			}
		})
	}
}
