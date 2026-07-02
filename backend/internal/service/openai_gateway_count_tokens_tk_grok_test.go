//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestResolveGrokInputTokensUpstream_OAuthUsesResponsesInputTokensPath(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:       61,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://api.x.ai/v1",
		},
	}

	got, err := svc.resolveGrokInputTokensUpstream(account)
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses/input_tokens", got)
}

func TestResolveGrokInputTokensUpstream_RelayUsesOpenAICompatBase(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
	}
	account := &Account{
		ID:       62,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "relay-key",
			"base_url": "http://edge.example/v1",
		},
	}

	got, err := svc.resolveGrokInputTokensUpstream(account)
	require.NoError(t, err)
	require.Equal(t, "http://edge.example/v1/responses/input_tokens", got)
}
