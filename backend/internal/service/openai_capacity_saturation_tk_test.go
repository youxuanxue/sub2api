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
		7: saturationThreshold,
		8: 0,
	}})

	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
		{account: &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
	}
	svc.computeOpenAISaturationPenalties(context.Background(), candidates)
	require.Equal(t, openAISaturationScorePenalty+float64(saturationThreshold), candidates[0].saturationScorePenalty)
	require.Zero(t, candidates[1].saturationScorePenalty)
	resetOpenAISatCache()
}

func TestOpenAIAccount429PressureSwitchesWithoutSaturationPenalty(t *testing.T) {
	account := &Account{ID: 283, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	sat := &fakeOpenAISaturationCounterRL{}
	limits := NewRateLimitService(&oauth429RateLimitRepo{}, nil, &config.Config{}, nil, nil)
	limits.SetOpenAISaturationCounter(sat)
	svc := &OpenAIGatewayService{rateLimitService: limits}
	limits.SetAccountRuntimeBlocker(svc)
	body := []byte(`{"detail":"Rate limit exceeded"}`)
	for i := int64(0); i < saturationThreshold; i++ {
		svc.handleOpenAIAccountUpstreamError(context.Background(), account, 429, nil, body)
	}
	require.Zero(t, sat.count, "429 feedback belongs only to the unified candidate failure counter")
	failure := svc.newOpenAIAccountFailoverError(account, 429, nil, body, "Rate limit exceeded", false, true)
	require.False(t, failure.RetryableOnSameAccount)
	require.Equal(t, candidateAccountRateLimitReason, failure.Reason)
	require.True(t, candidateFailureAttributable(failure))
	require.True(t, failure.SameAccountRetryDeadline.IsZero())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "soft preference does not hard-disable an account")

	for _, tc := range []struct {
		name    string
		account *Account
		headers http.Header
		body    []byte
	}{
		{"quota", account, codex7dExhaustedHeaders(600000), body},
		{"spark", account, http.Header{http.CanonicalHeaderKey(codexActiveLimitHeader): []string{codexBengalfoxActiveLimit}}, body},
		{"api_key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, nil, body},
		{"echoed_input", account, nil, []byte(`{"input":"Rate limit exceeded","error":{"message":"Invalid request"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, isOpenAIAccountRateLimitPressure(tc.account, tc.headers, tc.body))
			excluded := svc.newOpenAIAccountFailoverErrorWithClassificationHeaders(tc.account, 429, nil, tc.headers, tc.body, "", false, false)
			require.False(t, candidateFailureAttributable(excluded))
		})
	}
}

type cancellationAwareOpenAISaturation struct {
	fakeOpenAISaturationCounterRL
	contextErr error
}

func (c *cancellationAwareOpenAISaturation) IncrementSaturation(ctx context.Context, id int64, window int) (int64, error) {
	c.contextErr = ctx.Err()
	if c.contextErr != nil {
		return 0, c.contextErr
	}
	return c.fakeOpenAISaturationCounterRL.IncrementSaturation(ctx, id, window)
}
func TestOpenAISaturationSurvivesClientCancellation(t *testing.T) {
	counter := &cancellationAwareOpenAISaturation{}
	svc := &RateLimitService{openaiSaturationCounter: counter}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, int64(1), svc.recordOpenAIStubSaturation(ctx, 283, 503, "stream_capacity"))
	require.NoError(t, counter.contextErr)
	require.Equal(t, []int64{283}, counter.incrementIDs)
}
