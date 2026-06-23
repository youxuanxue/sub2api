package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBuildOpenAIV1SegmentURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.openai.com/v1/embeddings", buildOpenAIV1SegmentURL("", "embeddings"))
	require.Equal(t, "https://api.openai.com/v1/images/generations", buildOpenAIV1SegmentURL("", "images/generations"))
	require.Equal(t, "https://api.example.com/v1/embeddings", buildOpenAIV1SegmentURL("https://api.example.com/v1", "embeddings"))
	require.Equal(t, "https://api.example.com/v1/embeddings", buildOpenAIV1SegmentURL("https://api.example.com/v1/responses", "embeddings"))
}

func TestGrokAPIKeyRelayBuildsOpenAIV1TargetURLFromEdgeBaseURL(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled: false,
			},
		},
	}}
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}

	token, kind, err := svc.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "edge-grok-key", token)
	require.Equal(t, "apikey", kind)

	url, err := svc.buildOpenAIV1TargetURL(account, "embeddings")
	require.NoError(t, err)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/embeddings", url)
}

func TestGrokAPIKeyRelayBuildOpenAIV1TargetURLRequiresBaseURL(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:       406,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "edge-grok-key",
		},
	}

	_, err := svc.buildOpenAIV1TargetURL(account, "embeddings")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing base_url")
}
