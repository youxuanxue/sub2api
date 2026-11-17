//go:build unit

package service

import "testing"

func TestPreferUsageUpstreamModel_ThinkingTierWireWinsOverPlanFloor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		forwarded string
		planned   string
		want      string
	}{
		{
			name:      "high wire over medium floor",
			forwarded: "gemini-4-argon-high",
			planned:   "gemini-4-argon-medium",
			want:      "gemini-4-argon-high",
		},
		{
			name:      "medium wire matches floor",
			forwarded: "gemini-4-argon-medium",
			planned:   "gemini-4-argon-medium",
			want:      "gemini-4-argon-medium",
		},
		{
			name:      "empty wire falls back to plan",
			forwarded: "",
			planned:   "gemini-4-argon-medium",
			want:      "gemini-4-argon-medium",
		},
		{
			name:      "low wire over medium floor",
			forwarded: "gemini-4-argon-low",
			planned:   "gemini-4-argon-medium",
			want:      "gemini-4-argon-low",
		},
		{
			name:      "bare plan vs high wire",
			forwarded: "gemini-4-argon-high",
			planned:   "gemini-4-argon",
			want:      "gemini-4-argon-high",
		},
		{
			name:      "non-gemini disagreement keeps plan SSOT",
			forwarded: "gpt-5",
			planned:   "gpt-5.4",
			want:      "gpt-5.4",
		},
		{
			name:      "different gemini families keep plan",
			forwarded: "gemini-3.8-flash-high",
			planned:   "gemini-4-argon-medium",
			want:      "gemini-4-argon-medium",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := preferUsageUpstreamModel(tc.forwarded, tc.planned); got != tc.want {
				t.Fatalf("preferUsageUpstreamModel(%q, %q) = %q, want %q", tc.forwarded, tc.planned, got, tc.want)
			}
		})
	}
}
