//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsOpenAIImageCapabilityLoss400(t *testing.T) {
	t.Parallel()

	require.True(t, isOpenAIImageCapabilityLoss400(http.StatusBadRequest, []byte(
		`{"error":{"message":"The image_generation tool is not supported for this account."}}`,
	)))
	require.True(t, isOpenAIImageCapabilityLoss400(http.StatusBadRequest, []byte(
		`{"error":{"message":"image_generation is not available"}}`,
	)))
	require.False(t, isOpenAIImageCapabilityLoss400(http.StatusTooManyRequests, []byte(
		`{"error":{"message":"The image_generation tool is not supported for this account."}}`,
	)))
	require.False(t, isOpenAIImageCapabilityLoss400(http.StatusBadRequest, []byte(
		`{"error":{"message":"Invalid value for 'size'."}}`,
	)))
	require.False(t, isOpenAIImageCapabilityLoss400(http.StatusBadRequest, []byte(
		`{"error":{"type":"invalid_request_error","message":"Missing required parameter: 'prompt'."}}`,
	)))
}

func TestRateLimitService_HandleOpenAIImageCapabilityLoss400_CoolsCapabilityOnly(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 4466, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	body := []byte(`{"error":{"message":"The image_generation tool is not supported for this account."}}`)

	before := time.Now()
	handled := svc.HandleOpenAIImageCapabilityLoss400(context.Background(), account, http.StatusBadRequest, body)

	require.True(t, handled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, account.ID, call.accountID)
	require.Equal(t, openAIImageGenerationRateLimitKey, call.scope)
	require.Equal(t, openAIImageCapabilityLossReason, call.reason)
	require.WithinDuration(t, before.Add(24*time.Hour), call.resetAt, 2*time.Second)
	require.Zero(t, repo.tempCalls)
}

func TestRateLimitService_HandleOpenAIImageCapabilityLoss400_IgnoresCallerInvalidRequest(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 4467, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	body := []byte(`{"error":{"type":"invalid_request_error","message":"Invalid value for 'size'."}}`)

	handled := svc.HandleOpenAIImageCapabilityLoss400(context.Background(), account, http.StatusBadRequest, body)
	require.False(t, handled)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAIGatewayService_HandleOpenAIAccountUpstreamError_ImageCapability400DoesNotBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := &Account{ID: 4468, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	body := []byte(`{"error":{"message":"The image_generation tool is not supported for this account."}}`)

	disabled := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body, "gpt-image-2")
	require.False(t, disabled)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, openAIImageGenerationRateLimitKey, repo.modelRateLimitCalls[0].scope)
	_, wholeAccountBlocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, wholeAccountBlocked)
	require.Zero(t, repo.tempCalls)
}
