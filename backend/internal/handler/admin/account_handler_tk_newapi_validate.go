package admin

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/engine"
)

// tkValidateAccountPlatform rejects an account whose platform is not a real
// scheduling platform. The group create/update path already enforces this via a
// binding `oneof`, but the account path historically only had `binding:"required"`
// (no allowlist), so a typo'd or invented platform string (e.g. "volcengine"
// instead of the newapi platform + channel_type=45) was stored verbatim. Such an
// account can never join any scheduling pool — pool membership is strict platform
// equality (engine.IsOpenAICompatPoolMember / per-platform buckets) — so it
// silently fails with empty-pool 429s ("No available accounts") and no hint that
// the platform string itself is invalid. Validating against
// engine.AllSchedulingPlatforms() (the single source of truth for "platforms that
// have a scheduling pool") closes that asymmetry; adding a future platform there
// extends this check automatically.
func tkValidateAccountPlatform(platform string) string {
	for _, p := range engine.AllSchedulingPlatforms() {
		if platform == p {
			return ""
		}
	}
	return fmt.Sprintf(
		"platform %q is not a valid account platform (must be one of: %s)",
		platform, strings.Join(engine.AllSchedulingPlatforms(), ", "),
	)
}

// tkValidateNewAPIAccountCreate returns a user-facing error message, or empty if OK.
// Called by all three account-creation paths (single Create, BatchCreate, import),
// so the platform-allowlist check below guards every persisted account.
func tkValidateNewAPIAccountCreate(platform string, channelType int, credentials map[string]any) string {
	if msg := tkValidateAccountPlatform(platform); msg != "" {
		return msg
	}
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
