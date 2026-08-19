package usagestats

import "strings"

const PublicPlatformGoogle = "google"

// NormalizePublicPlatform maps internal routing sources to the platform names
// exposed by customer and business usage views.
func NormalizePublicPlatform(platform string) string {
	trimmed := strings.TrimSpace(platform)
	switch strings.ToLower(trimmed) {
	case "gemini", "antigravity", PublicPlatformGoogle:
		return PublicPlatformGoogle
	default:
		return trimmed
	}
}

// NormalizePlatformDashboardStats canonicalizes public platform names and
// merges metrics that originated from multiple internal routing sources.
func NormalizePlatformDashboardStats(rows []PlatformDashboardStats) []PlatformDashboardStats {
	if len(rows) == 0 {
		return nil
	}
	result := make([]PlatformDashboardStats, 0, len(rows))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		platform := NormalizePublicPlatform(row.Platform)
		if platform == "" {
			continue
		}
		if i, ok := index[platform]; ok {
			result[i].TotalRequests += row.TotalRequests
			result[i].TotalTokens += row.TotalTokens
			result[i].TotalActualCost += row.TotalActualCost
			result[i].TodayRequests += row.TodayRequests
			result[i].TodayTokens += row.TodayTokens
			result[i].TodayActualCost += row.TodayActualCost
			continue
		}
		row.Platform = platform
		index[platform] = len(result)
		result = append(result, row)
	}
	return result
}

// NormalizePlatformUsage applies the same public taxonomy to compact cost rows.
func NormalizePlatformUsage(rows []PlatformUsage) []PlatformUsage {
	if len(rows) == 0 {
		return nil
	}
	result := make([]PlatformUsage, 0, len(rows))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		platform := NormalizePublicPlatform(row.Platform)
		if platform == "" {
			continue
		}
		if i, ok := index[platform]; ok {
			result[i].TodayActualCost += row.TodayActualCost
			result[i].TotalActualCost += row.TotalActualCost
			continue
		}
		row.Platform = platform
		index[platform] = len(result)
		result = append(result, row)
	}
	return result
}
