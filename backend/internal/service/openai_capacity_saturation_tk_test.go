//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAINativeCapacityUnavailable(t *testing.T) {
	t.Parallel()

	overloaded := []byte(`{"error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}`)
	require.True(t, isOpenAINativeCapacityUnavailable(http.StatusServiceUnavailable, "", overloaded))
	require.False(t, isOpenAINativeCapacityUnavailable(http.StatusBadRequest, "", overloaded),
		"non-503 capacity shed is request-scoped and must not saturate the account")

	temp503 := []byte(`{"error":{"type":"service_unavailable_error","message":"Service temporarily unavailable"}}`)
	require.True(t, isOpenAINativeCapacityUnavailable(http.StatusServiceUnavailable, "", temp503))
	require.False(t, isOpenAINativeCapacityUnavailable(http.StatusBadGateway, "", temp503),
		"non-503 temporarily unavailable must not match without overloaded wording")

	tkWrap := []byte(`{"error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`)
	require.False(t, isOpenAINativeCapacityUnavailable(http.StatusServiceUnavailable, "", tkWrap),
		"TokenKey sanitized envelope must not count as native OAuth capacity")
	require.False(t, isOpenAINativeCapacityUnavailable(http.StatusBadGateway, "", tkWrap))
}

func TestShouldRecordOpenAICapacitySaturation_StubSeesSanitizedEnvelope(t *testing.T) {
	t.Parallel()

	stub := openAIEdgeStub(63)
	oauth := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	tkWrap := []byte(`{"error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`)

	require.True(t, shouldRecordOpenAICapacitySaturation(stub, http.StatusBadGateway, "", tkWrap),
		"prod stub must soft-penalize sanitized edge capacity failures")
	require.False(t, shouldRecordOpenAICapacitySaturation(oauth, http.StatusBadGateway, "", tkWrap),
		"OAuth must ignore TokenKey sanitized envelope")

	overloaded := []byte(`{"error":{"message":"Our servers are currently overloaded. Please try again later."}}`)
	require.True(t, shouldRecordOpenAICapacitySaturation(oauth, http.StatusServiceUnavailable, "", overloaded))
	require.True(t, shouldRecordOpenAICapacitySaturation(stub, http.StatusServiceUnavailable, "", overloaded))
}

func TestHandleOpenAIAccountUpstreamError_NativeCapacityIncrementsSaturationNoCooldown(t *testing.T) {
	repo := &capacityShedAccountRepoStub{}
	sat := &fakeOpenAISaturationCounterRL{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitService.SetOpenAISaturationCounter(sat)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}

	payload := []byte(`{"error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}`)

	t.Run("oauth", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		require.False(t, gateway.handleOpenAIAccountUpstreamError(
			context.Background(), account, http.StatusServiceUnavailable, nil, payload, "gpt-5.5"))
		require.Zero(t, repo.tempUnschedCalls)
		require.Equal(t, []int64{7}, sat.incrementIDs)
	})

	t.Run("stub_sanitized_envelope", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		wrap := []byte(`{"error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`)
		require.False(t, gateway.handleOpenAIAccountUpstreamError(
			context.Background(), openAIEdgeStub(63), http.StatusBadGateway, nil, wrap, "gpt-5.5"))
		require.Zero(t, repo.tempUnschedCalls)
		require.Equal(t, []int64{63}, sat.incrementIDs)
	})

	t.Run("oauth_ignores_sanitized_envelope", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		wrap := []byte(`{"error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`)
		account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		_ = gateway.handleOpenAIAccountUpstreamError(
			context.Background(), account, http.StatusBadGateway, nil, wrap, "gpt-5.5")
		require.Empty(t, sat.incrementIDs)
	})
}

func TestHandleOpenAIStreamTerminalAccountSideEffects_CapacityIncrementsSaturation(t *testing.T) {
	repo := &capacityShedAccountRepoStub{}
	sat := &fakeOpenAISaturationCounterRL{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitService.SetOpenAISaturationCounter(sat)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}

	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`)
	message := "Our servers are currently overloaded. Please try again later."

	t.Run("oauth_response_failed", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		status, disabled := gateway.handleOpenAIStreamTerminalAccountSideEffects(
			nil, account, payload, message, nil, "gpt-5.6-terra")
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.False(t, disabled)
		require.Zero(t, repo.tempUnschedCalls)
		require.Equal(t, []int64{9}, sat.incrementIDs,
			"stream capacity must feed the same saturation SSOT as HTTP 503 even when failover is blocked")
	})

	t.Run("stub_response_failed", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		status, disabled := gateway.handleOpenAIStreamTerminalAccountSideEffects(
			nil, openAIEdgeStub(63), payload, message, nil, "gpt-5.6-terra")
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.False(t, disabled)
		require.Equal(t, []int64{63}, sat.incrementIDs)
	})

	t.Run("non_capacity_stream_failure_skips", func(t *testing.T) {
		*sat = fakeOpenAISaturationCounterRL{}
		ctxWin := []byte(`{"type":"response.failed","response":{"error":{"code":"invalid_request_error","message":"Your input exceeds the context window of this model."}}}`)
		account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		status, disabled := gateway.handleOpenAIStreamTerminalAccountSideEffects(
			nil, account, ctxWin, "Your input exceeds the context window of this model.", nil, "gpt-5.6-terra")
		require.NotEqual(t, http.StatusServiceUnavailable, status)
		require.False(t, disabled)
		require.Empty(t, sat.incrementIDs, "non-capacity stream failures must not feed saturation")
	})
}

func TestComputeOpenAISaturationPenalties_DeprioritizesOAuthAccount(t *testing.T) {
	resetOpenAISatCache()
	svc := &OpenAIGatewayService{}
	svc.SetOpenAISaturationCounter(&fakeSaturationCache{counts: map[int64]int64{
		7: openAIEdgeMirrorStubSaturationThreshold,
		8: 0,
	}})

	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
		{account: &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
	}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, openAISaturationScorePenalty+float64(openAIEdgeMirrorStubSaturationThreshold), candidates[0].saturationScorePenalty)
	require.Zero(t, candidates[1].saturationScorePenalty)
	resetOpenAISatCache()
}
