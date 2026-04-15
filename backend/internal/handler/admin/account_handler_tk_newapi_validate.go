package admin

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// tkValidateNewAPIAccountCreate returns a user-facing error message, or empty if OK.
func tkValidateNewAPIAccountCreate(platform string, channelType int, credentials map[string]any) string {
	if channelType < 0 {
		return "channel_type must be >= 0"
	}
	if platform == domain.PlatformNewAPI {
		if channelType <= 0 {
			return "channel_type must be > 0 for newapi platform"
		}
		if baseURL, _ := credentials["base_url"].(string); strings.TrimSpace(baseURL) == "" {
			return "credentials.base_url is required for newapi platform"
		}
	}
	return ""
}

// tkValidateAccountChannelTypePtr returns an error message when channel_type pointer is negative.
func tkValidateAccountChannelTypePtr(channelType *int) string {
	if channelType != nil && *channelType < 0 {
		return "channel_type must be >= 0"
	}
	return ""
}
