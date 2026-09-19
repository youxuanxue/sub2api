//go:build unit

package service

import "testing"

// Empty GroupPlatform must fall back to req.Platform (normalized), never to a
// raw empty string that NormalizeOpenAICompatiblePlatform would map to openai
// and drop Grok/CN sticky owners (TK#1934 + sticky Grok/CN sentinel).
func TestOpenAIAccountScheduleRequest_SchedulePlatformEmptyGroupFallsBack(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		groupPlatform string
		platform      string
		want          string
	}{
		{name: "group wins", groupPlatform: PlatformGrok, platform: PlatformOpenAI, want: PlatformGrok},
		{name: "empty group uses platform", groupPlatform: "", platform: PlatformGrok, want: PlatformGrok},
		{name: "empty group keeps deepseek", groupPlatform: "", platform: PlatformDeepseek, want: PlatformDeepseek},
		{name: "both empty defaults openai", groupPlatform: "", platform: "", want: PlatformOpenAI},
		{name: "whitespace group treated empty", groupPlatform: "  ", platform: PlatformGrok, want: PlatformGrok},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := OpenAIAccountScheduleRequest{
				GroupPlatform: tc.groupPlatform,
				Platform:      tc.platform,
			}
			if got := req.schedulePlatform(); got != tc.want {
				t.Fatalf("schedulePlatform()=%q want %q", got, tc.want)
			}
		})
	}
}
