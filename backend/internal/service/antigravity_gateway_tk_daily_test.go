//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestTkAntigravityPaidTierUsesDaily_DelegatesToPlanTypeOwner(t *testing.T) {
	t.Parallel()

	require.False(t, tkAntigravityPaidTierUsesDaily(nil))
	require.False(t, tkAntigravityPaidTierUsesDaily(&Account{Platform: PlatformAntigravity}))

	for _, raw := range []string{"Free", "free-tier", "Pro", "Ultra", "g1-pro-tier", "g1-ultra-tier"} {
		want := antigravity.IsPaidPlanType(raw)
		require.Equal(t, want, tkAntigravityPaidTierUsesDaily(&Account{
			Platform:    PlatformAntigravity,
			Credentials: map[string]any{"plan_type": raw},
		}), "credentials.plan_type=%s", raw)
		require.Equal(t, want, tkAntigravityPaidTierUsesDaily(&Account{
			Platform: PlatformAntigravity,
			Extra:    map[string]any{"plan_type": raw},
		}), "extra.plan_type=%s", raw)
	}
}

func TestResolveAntigravityForwardBaseURL_PaidTierUsesDaily(t *testing.T) {
	t.Setenv(antigravityForwardBaseURLEnv, "")

	require.Equal(t, antigravity.ProdBaseURL(), antigravity.BaseURLs[0])
	require.Equal(t, antigravity.DailyBaseURL(), antigravity.BaseURLs[1])

	require.Equal(t, antigravity.ProdBaseURL(), resolveAntigravityForwardBaseURL(nil))
	require.Equal(t, antigravity.ProdBaseURL(), resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Free"},
	}))
	require.Equal(t, antigravity.DailyBaseURL(), resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Pro"},
	}))
}

func TestResolveAntigravityForwardBaseURL_EnvOverridesPaidTier(t *testing.T) {
	t.Setenv(antigravityForwardBaseURLEnv, "daily")
	require.Equal(t, antigravity.DailyBaseURL(), resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Free"},
	}))

	t.Setenv(antigravityForwardBaseURLEnv, "prod")
	require.Equal(t, antigravity.ProdBaseURL(), resolveAntigravityForwardBaseURL(&Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"plan_type": "Pro"},
	}))
}
