//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/engine"
	"github.com/stretchr/testify/require"
)

func TestTkValidateAccountPlatform(t *testing.T) {
	t.Run("every scheduling platform is accepted", func(t *testing.T) {
		for _, p := range engine.AllSchedulingPlatforms() {
			require.Empty(t, tkValidateAccountPlatform(p), "platform %q should be valid", p)
		}
		// Spot-check the literals so a future rename of a constant is caught here too.
		for _, p := range []string{
			domain.PlatformAnthropic, domain.PlatformOpenAI, domain.PlatformGemini,
			domain.PlatformAntigravity, domain.PlatformNewAPI, domain.PlatformKiro,
			domain.PlatformGrok,
		} {
			require.Empty(t, tkValidateAccountPlatform(p), "platform %q should be valid", p)
		}
	})

	t.Run("invalid platform strings are rejected", func(t *testing.T) {
		// "volcengine" is the canonical trap: it is a channel_type (45) on the
		// newapi platform, NOT a platform of its own. Persisting it silently
		// produced an account that never joins any scheduling pool.
		for _, bad := range []string{"volcengine", "doubao", "", "Anthropic", "newapi ", "vertex"} {
			msg := tkValidateAccountPlatform(bad)
			require.NotEmpty(t, msg, "platform %q should be rejected", bad)
			require.Contains(t, msg, "not a valid account platform")
		}
	})
}

func TestTkValidateNewAPIAccountCreate_PlatformAllowlist(t *testing.T) {
	t.Run("invalid platform rejected before newapi-specific checks", func(t *testing.T) {
		msg := tkValidateNewAPIAccountCreate("volcengine", 45, map[string]any{
			"base_url": "https://ark.cn-beijing.volces.com",
			"api_key":  "sk-x",
		})
		require.Contains(t, msg, "not a valid account platform")
	})

	t.Run("valid newapi account still enforces channel_type + base_url", func(t *testing.T) {
		// channel_type missing for newapi
		require.Contains(t,
			tkValidateNewAPIAccountCreate(domain.PlatformNewAPI, 0, map[string]any{"base_url": "https://x"}),
			"channel_type must be > 0",
		)
		// base_url missing for newapi
		require.Contains(t,
			tkValidateNewAPIAccountCreate(domain.PlatformNewAPI, 45, map[string]any{}),
			"base_url is required",
		)
		// fully valid newapi (VolcEngine video channel)
		require.Empty(t,
			tkValidateNewAPIAccountCreate(domain.PlatformNewAPI, 45, map[string]any{
				"base_url": "https://ark.cn-beijing.volces.com",
				"api_key":  "sk-x",
			}),
		)
	})

	t.Run("valid non-newapi account passes", func(t *testing.T) {
		require.Empty(t, tkValidateNewAPIAccountCreate(domain.PlatformAnthropic, 0, map[string]any{}))
	})
}
