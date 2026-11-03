//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func openAICreditExhaustedGateway(repo *openAIStream403AccountRepo) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		}},
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
}

func openAICreditExhaustedSSE(events ...string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-credit"}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(events, "\n") + "\n")),
	}
}

func TestOpenAIStreamCreditExhausted_UnwrittenBodyFailoversAndDisablesAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payloads := []struct {
		name string
		data string
	}{
		{
			name: "code credit_balance_exhausted",
			data: `data: {"type":"error","error":{"code":"credit_balance_exhausted","type":"billing_error","message":"Your credit balance is exhausted"}}`,
		},
		{
			name: "type insufficient_quota",
			data: `data: {"type":"error","error":{"type":"insufficient_quota","message":"quota"}}`,
		},
		{
			name: "nested response.error.code",
			data: `data: {"type":"response.failed","response":{"error":{"code":"insufficient_quota","message":"quota"}}}`,
		},
	}
	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			repo := &openAIStream403AccountRepo{}
			svc := openAICreditExhaustedGateway(repo)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			account := &Account{ID: 8478, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Name: "credit-key"}

			_, err := svc.handleStreamingResponse(c.Request.Context(), openAICreditExhaustedSSE(
				"event: response.created",
				`data: {"type":"response.created","response":{"id":"resp_credit"}}`,
				"",
				"event: error",
				tc.data,
				"",
			), c, account, time.Now(), "gpt-5", "gpt-5")

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
			require.True(t, failoverErr.ShouldRetryNextAccount())
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.False(t, failoverErr.RequestScopedTransient)
			require.Equal(t, 1, repo.setErrorCalls)
			require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

func TestOpenAIStreamCreditExhausted_PoolModeDoesNotRetrySameAPIKey(t *testing.T) {
	repo := &openAIStream403AccountRepo{}
	svc := openAICreditExhaustedGateway(repo)
	account := &Account{
		ID:       8479,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(http.StatusPaymentRequired)},
		},
	}
	payload := []byte(`{"type":"error","error":{"code":"credit_balance_exhausted","message":"balance gone"}}`)

	failoverErr := svc.newOpenAIStreamFailoverErrorWithModel(nil, account, false, "rid", payload, "balance gone", "gpt-5")

	require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 1, repo.setErrorCalls)
}

func TestOpenAIStreamCreditExhausted_MessageTextDoesNotDisable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &openAIStream403AccountRepo{}
	svc := openAICreditExhaustedGateway(repo)
	payload := []byte(`{"type":"error","error":{"code":"server_error","type":"server_error","message":"the prompt mentioned credit_balance_exhausted and insufficient_quota"}}`)
	message := extractOpenAISSEErrorMessage(payload)

	require.False(t, openAIStreamExplicitCreditExhausted(payload))
	require.Equal(t, http.StatusBadGateway, openAIStreamFailureStatus(payload, message))
	status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, &Account{
		ID: 8480, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
	}, payload, message, nil)
	require.Equal(t, http.StatusBadGateway, status)
	require.False(t, disabled)
	require.Zero(t, repo.setErrorCalls)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 8480, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, err := svc.handleStreamingResponse(c.Request.Context(), openAICreditExhaustedSSE(
		"event: error",
		`data: {"type":"error","error":{"code":"server_error","type":"server_error","message":"the prompt mentioned credit_balance_exhausted and insufficient_quota"}}`,
		"",
	), c, account, time.Now(), "gpt-5", "gpt-5")

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Zero(t, repo.setErrorCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIStreamCreditExhausted_OAuthAndSetupTokenDoNotSetError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			repo := &openAIStream403AccountRepo{}
			svc := openAICreditExhaustedGateway(repo)
			payload := []byte(`{"type":"error","error":{"type":"insufficient_quota","message":"weekly window"}}`)
			message := extractOpenAISSEErrorMessage(payload)
			account := &Account{ID: 8481, Platform: PlatformOpenAI, Type: accountType}

			status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, message, nil)
			require.Equal(t, http.StatusPaymentRequired, status)
			require.False(t, disabled)
			require.Zero(t, repo.setErrorCalls)
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			_, err := svc.handleStreamingResponse(context.Background(), openAICreditExhaustedSSE(
				"event: error",
				`data: {"type":"error","error":{"code":"credit_balance_exhausted","message":"balance gone"}}`,
				"",
			), c, account, time.Now(), "gpt-5", "gpt-5")
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusPaymentRequired, failoverErr.StatusCode)
			require.True(t, failoverErr.ShouldRetryNextAccount())
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.Zero(t, repo.setErrorCalls)
		})
	}
}

func TestOpenAIStreamCreditExhausted_WrittenBodyUpdatesAccountWithoutReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &openAIStream403AccountRepo{}
	svc := openAICreditExhaustedGateway(repo)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{ID: 8482, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := svc.handleStreamingResponse(c.Request.Context(), openAICreditExhaustedSSE(
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"code":"credit_balance_exhausted","message":"balance gone"}}`,
		"",
	), c, account, time.Now(), "gpt-5", "gpt-5")

	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Equal(t, 1, repo.setErrorCalls)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Contains(t, rec.Body.String(), "hi")
}

func TestOpenAIStreamCreditExhausted_Ordinary429StaysRateLimit(t *testing.T) {
	payload := []byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down; the prompt said insufficient_quota and credit_balance_exhausted"}}`)
	message := extractOpenAISSEErrorMessage(payload)

	require.False(t, openAIStreamExplicitCreditExhausted(payload))
	require.Equal(t, http.StatusTooManyRequests, openAIStreamFailureStatus(payload, message))
	require.Equal(t, http.StatusTooManyRequests, openAIStreamFailedEventSemanticStatus(payload, message))
	require.True(t, openAIStreamErrorEventShouldFailover(payload, message))

	svc := &OpenAIGatewayService{}
	account := &Account{ID: 8483, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	status, disabled := svc.handleOpenAIStreamTerminalAccountSideEffects(nil, account, payload, message, nil)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.False(t, disabled)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	err := svc.newOpenAIAccountFailoverError(account, status, nil, payload, message, disabled, false)
	require.True(t, err.RetryableOnSameAccount)
}
