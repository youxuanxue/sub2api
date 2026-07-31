package service

import (
	"fmt"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
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
		{
			name:       "agent_plan_invalid_action_uses_anthropic_estimate",
			account:    newAgentPlanInputTokensFallbackAccount(newapiintegration.VolcEngineAgentPlanBaseURL),
			statusCode: http.StatusNotFound,
			body:       `{"error":{"code":"InvalidAction","message":"The specified action is invalid: /api/v3/responses/input_tokens","type":"NotFound"}}`,
			want:       openAIInputTokensFallbackAnthropicEstimate,
		},
		{
			name:       "agent_plan_not_found_type_without_code_uses_anthropic_estimate",
			account:    newAgentPlanInputTokensFallbackAccount(newapiintegration.VolcEngineAgentPlanBaseURL),
			statusCode: http.StatusNotFound,
			body:       `{"error":{"message":"The specified action is invalid: /api/v3/responses/input_tokens","type":"NotFound"}}`,
			want:       openAIInputTokensFallbackAnthropicEstimate,
		},
		{
			name:       "volcengine_payg_invalid_action_stays_upstream_error",
			account:    newAgentPlanInputTokensFallbackAccount("https://ark.cn-beijing.volces.com/api/v3"),
			statusCode: http.StatusNotFound,
			body:       `{"error":{"code":"InvalidAction","message":"The specified action is invalid: /api/v3/responses/input_tokens","type":"NotFound"}}`,
			want:       openAIInputTokensFallbackNone,
		},
		{
			name:       "agent_plan_invalid_model_stays_upstream_error",
			account:    newAgentPlanInputTokensFallbackAccount(newapiintegration.VolcEngineAgentPlanBaseURL),
			statusCode: http.StatusNotFound,
			body:       `{"error":{"code":"InvalidEndpointOrModel","message":"The model does not exist","type":"NotFound"}}`,
			want:       openAIInputTokensFallbackNone,
		},
		{
			name:       "agent_plan_unrelated_invalid_action_stays_upstream_error",
			account:    newAgentPlanInputTokensFallbackAccount(newapiintegration.VolcEngineAgentPlanBaseURL),
			statusCode: http.StatusNotFound,
			body:       `{"error":{"code":"InvalidAction","message":"The specified action is invalid: /api/v3/models","type":"NotFound"}}`,
			want:       openAIInputTokensFallbackNone,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyOpenAIInputTokensFallback(tt.account, tt.statusCode, []byte(tt.body))
			require.Equal(t, tt.want, got.Kind)
		})
	}
}

func newAgentPlanInputTokensFallbackAccount(baseURL string) *Account {
	return &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{"base_url": baseURL},
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
