//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestTkAntigravityPaidTierUsesDaily(t *testing.T) {
	t.Parallel()

	require.False(t, tkAntigravityPaidTierUsesDaily(nil))
	require.False(t, tkAntigravityPaidTierUsesDaily(&Account{Platform: PlatformAntigravity}))
	require.False(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Free"},
	}))
	require.False(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "free-tier"},
	}))
	require.True(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Pro"},
	}))
	require.True(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Ultra"},
	}))
	require.True(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "g1-pro-tier"},
	}))
	require.True(t, tkAntigravityPaidTierUsesDaily(&Account{
		Platform: PlatformAntigravity,
		Extra:    map[string]any{"plan_type": "g1-ultra-tier"},
	}))
}

func TestResolveAntigravityForwardBaseURL_PaidTierUsesDaily(t *testing.T) {
	t.Setenv(antigravityForwardBaseURLEnv, "")

	const officialDaily = "https://daily-cloudcode-pa.googleapis.com"
	prod := antigravity.BaseURLs[0]
	require.Equal(t, "https://cloudcode-pa.googleapis.com", prod)
	require.Equal(t, officialDaily, antigravity.BaseURLs[1])

	require.Equal(t, prod, resolveAntigravityForwardBaseURL(nil))
	require.Equal(t, prod, resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Free"},
	}))
	require.Equal(t, officialDaily, resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Pro"},
	}))
}

func TestResolveAntigravityForwardBaseURL_EnvOverridesPaidTier(t *testing.T) {
	t.Setenv(antigravityForwardBaseURLEnv, "daily")
	require.Equal(t, "https://daily-cloudcode-pa.googleapis.com", resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Free"},
	}))

	t.Setenv(antigravityForwardBaseURLEnv, "prod")
	require.Equal(t, antigravity.BaseURLs[0], resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Pro"},
	}))
}
