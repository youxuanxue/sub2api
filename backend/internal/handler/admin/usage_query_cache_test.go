package admin

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageStatsCacheKey_StableAndDistinct(t *testing.T) {
	start := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	base := usagestats.UsageLogFilters{StartTime: &start, EndTime: &end, Model: "claude-3"}

	k1 := usageStatsCacheKey(base)
	k2 := usageStatsCacheKey(base)
	require.NotEmpty(t, k1)
	require.Equal(t, k1, k2, "same filters must produce same key")

	other := base
	other.Model = "gpt-4o"
	require.NotEqual(t, k1, usageStatsCacheKey(other), "different model must change key")

	withUser := base
	withUser.UserID = 7
	require.NotEqual(t, k1, usageStatsCacheKey(withUser), "different user must change key")

	withoutEndpointStats := base
	withoutEndpointStats.SkipEndpointStats = true
	require.NotEqual(t, k1, usageStatsCacheKey(withoutEndpointStats), "endpoint stats toggle must change key")

	withoutSummary := base
	withoutSummary.SkipSummary = true
	require.NotEqual(t, k1, usageStatsCacheKey(withoutSummary), "summary toggle must change key")

	inboundOnly := base
	inboundOnly.EndpointStatsSource = usagestats.EndpointSourceInbound
	require.NotEqual(t, k1, usageStatsCacheKey(inboundOnly), "endpoint stats source must change key")
}
