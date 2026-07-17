package service

import (
	"fmt"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpenAIInputTokensFallback(t *testing.T) {
	cases := []struct {
		name       string
		account    *Account
		statusCode int
		body       string
		want       openAIInputTokensFallbackKind
	}{
		{
			name:       "oauth_missing_scope_uses_tiktoken_estimate",
			account:    &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI},
			statusCode: http.StatusForbidden,
			body:       `{"error":{"code":"missing_scope","message":"Missing scopes: api.responses.write"}}`,
			want:       openAIInputTokensFallbackOAuthEstimate,
		},
		{
			name:       "oauth_plain_unauthorized_does_not_estimate",
			account:    &Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI},
			statusCode: http.StatusUnauthorized,
			body:       `{"error":{"type":"authentication_error","message":"unauthorized"}}`,
			want:       openAIInputTokensFallbackNone,
		},
		{
			name:       "api_key_input_tokens_404_uses_anthropic_estimate",
			account:    &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI},
			statusCode: http.StatusNotFound,
			body:       `{"error":{"message":"The /v1/responses/input_tokens endpoint was not found"}}`,
			want:       openAIInputTokensFallbackAnthropicEstimate,
		},
		{
			name:       "api_key_bare_404_uses_anthropic_estimate",
			account:    &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI},
			statusCode: http.StatusNotFound,
			body:       `{"message":"Not Found"}`,
			want:       openAIInputTokensFallbackAnthropicEstimate,
		},
		{
			name:       "policy_403_envelope_uses_anthropic_estimate",
			account:    &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI},
			statusCode: http.StatusBadGateway,
			body:       `{"error":{"message":"Upstream returned 403 for this request. This is an upstream access/policy rejection unrelated to request size."}}`,
			want:       openAIInputTokensFallbackAnthropicEstimate,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyOpenAIInputTokensFallback(tt.account, tt.statusCode, []byte(tt.body))
			require.Equal(t, tt.want, got.Kind)
		})
	}
}

func TestShouldEstimateOpenAIInputTokensForAuthError(t *testing.T) {
	require.True(t, shouldEstimateOpenAIInputTokensForAuthError(
		&Account{Type: AccountTypeAPIKey, Platform: PlatformNewAPI},
		fmt.Errorf("api_key not found"),
	))
	require.True(t, shouldEstimateOpenAIInputTokensForAuthError(
		&Account{Type: AccountTypeServiceAccount, Platform: PlatformNewAPI, ChannelType: newapiconstant.ChannelTypeVertexAi},
		fmt.Errorf("invalid private key"),
	))
	require.False(t, shouldEstimateOpenAIInputTokensForAuthError(
		&Account{Type: AccountTypeOAuth, Platform: PlatformOpenAI},
		fmt.Errorf("unauthorized"),
	))
}
