package usagestats

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizePublicPlatform(t *testing.T) {
	tests := map[string]string{
		"anthropic":   "anthropic",
		"openai":      "openai",
		"gemini":      "google",
		"antigravity": "google",
		"google":      "google",
	}

	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, want, NormalizePublicPlatform(input))
		})
	}
}

func TestNormalizePlatformDashboardStats_MergesGoogleSources(t *testing.T) {
	rows := NormalizePlatformDashboardStats([]PlatformDashboardStats{
		{Platform: "anthropic", TotalRequests: 2, TotalTokens: 20, TotalActualCost: 2, TodayRequests: 1, TodayTokens: 10, TodayActualCost: 1},
		{Platform: "gemini", TotalRequests: 3, TotalTokens: 30, TotalActualCost: 3, TodayRequests: 2, TodayTokens: 20, TodayActualCost: 2},
		{Platform: "antigravity", TotalRequests: 5, TotalTokens: 50, TotalActualCost: 5, TodayRequests: 4, TodayTokens: 40, TodayActualCost: 4},
	})

	require.Equal(t, []PlatformDashboardStats{
		{Platform: "anthropic", TotalRequests: 2, TotalTokens: 20, TotalActualCost: 2, TodayRequests: 1, TodayTokens: 10, TodayActualCost: 1},
		{Platform: "google", TotalRequests: 8, TotalTokens: 80, TotalActualCost: 8, TodayRequests: 6, TodayTokens: 60, TodayActualCost: 6},
	}, rows)
}

func TestNormalizePlatformUsage_MergesGoogleSources(t *testing.T) {
	rows := NormalizePlatformUsage([]PlatformUsage{
		{Platform: "gemini", TodayActualCost: 1.25, TotalActualCost: 3.5},
		{Platform: "antigravity", TodayActualCost: 2.75, TotalActualCost: 4.5},
		{Platform: "openai", TodayActualCost: 3, TotalActualCost: 9},
	})

	require.Equal(t, []PlatformUsage{
		{Platform: "google", TodayActualCost: 4, TotalActualCost: 8},
		{Platform: "openai", TodayActualCost: 3, TotalActualCost: 9},
	}, rows)
}
