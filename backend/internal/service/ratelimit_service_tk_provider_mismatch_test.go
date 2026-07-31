//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestRateLimitService_AgentPlanOpenAIProviderMismatchSkipsPermanentDisable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newAgentPlanRateLimitAccountForTest(newapiintegration.VolcEngineAgentPlanBaseURL)
	body := []byte(`{"error":{"message":"Incorrect API key provided: ark-test. You can find your API key at https://platform.openai.com/account/api-keys."}}`)

	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.False(t, shouldDisable)
	require.Zero(t, repo.setErrorCalls, "a provider-routing mismatch must not poison account health")
}

func TestRateLimitService_AgentPlanGenuineArk401StillPermanentlyDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newAgentPlanRateLimitAccountForTest(newapiintegration.VolcEngineAgentPlanBaseURL)
	body := []byte(`{"error":{"message":"Invalid API key"}}`)

	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "a genuine Agent Plan credential failure must retain the auth guard")
}

func TestRateLimitService_VolcEnginePayAsYouGoOpenAI401StillPermanentlyDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newAgentPlanRateLimitAccountForTest("https://ark.cn-beijing.volces.com/api/v3")
	body := []byte(`{"error":{"message":"Incorrect API key provided: ark-test. You can find your API key at https://platform.openai.com/account/api-keys."}}`)

	shouldDisable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "the exception must require the Agent Plan base_url property")
}

func newAgentPlanRateLimitAccountForTest(baseURL string) *Account {
	return &Account{
		ID:          8801,
		Name:        "volcengine-agent-plan-property-scope",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":  "ark-test",
			"base_url": baseURL,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}
