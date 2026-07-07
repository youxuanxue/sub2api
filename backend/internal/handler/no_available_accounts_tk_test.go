package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTkSelectFailureStatusMessage verifies the empty-pool fast-fail mapping
// shared by the openai/newapi compat handlers (#575 parity with the anthropic
// path): the no-available-accounts error family becomes 429 + Retry-After,
// everything else (DB outage, ...) stays 503.
func TestTkSelectFailureStatusMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newCtx := func(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c, w
	}

	t.Run("no_available_accounts_sentinel_returns_429_with_retry_after", func(t *testing.T) {
		c, w := newCtx(t)
		status, errType, msg := tkSelectFailureStatusMessage(c, service.ErrNoAvailableAccounts, "gpt-5.2")

		require.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, "api_error", errType)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		assert.Contains(t, msg, "No available accounts")
	})

	t.Run("channel_pricing_restriction_returns_400", func(t *testing.T) {
		c, w := newCtx(t)
		wrapped := fmt.Errorf("%w: gpt-5.2 (channel pricing restriction)", service.ErrUnsupportedModel)
		status, errType, msg := tkSelectFailureStatusMessage(c, wrapped, "gpt-5.2")

		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, service.TkUnsupportedModelErrType, errType)
		assert.Equal(t, service.TkUnsupportedModelMessage("gpt-5.2"), msg)
		assert.Empty(t, w.Header().Get("Retry-After"))
	})

	t.Run("deprecated_anthropic_model_returns_400_without_retry_after", func(t *testing.T) {
		c, w := newCtx(t)
		wrapped := fmt.Errorf("%w: claude-3-5-sonnet-20241022 (suggest %q)", service.ErrDeprecatedAnthropicModel, "claude-sonnet-4-6")
		status, errType, msg := tkSelectFailureStatusMessage(c, wrapped, "claude-3-5-sonnet-20241022")

		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, service.TkDeprecatedAnthropicErrorType, errType)
		assert.Contains(t, msg, "claude-3-5-sonnet-20241022")
		assert.Contains(t, msg, "retired")
		assert.Empty(t, w.Header().Get("Retry-After"))
	})

	t.Run("no_available_accounts_with_deprecated_model_returns_400", func(t *testing.T) {
		c, w := newCtx(t)
		status, errType, msg := tkSelectFailureStatusMessage(c, service.ErrNoAvailableAccounts, "claude-3-5-haiku-20241022")

		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, service.TkDeprecatedAnthropicErrorType, errType)
		assert.Contains(t, msg, "claude-3-5-haiku-20241022")
		assert.Empty(t, w.Header().Get("Retry-After"))
	})

	t.Run("unsupported_model_returns_400_invalid_request", func(t *testing.T) {
		// The scheduler determined no account in the pool serves this model NAME
		// (e.g. a client sending "deepseek-chat" to a pool mapping only
		// "deepseek-v4-*"). That is a client error → 400 invalid_request_error, NOT
		// an empty-pool 429, so it never reads as a capacity signal / fires alerts.
		c, w := newCtx(t)
		wrapped := fmt.Errorf("%w: deepseek-chat (total=1 eligible=0 ...)", service.ErrUnsupportedModel)
		status, errType, msg := tkSelectFailureStatusMessage(c, wrapped, "deepseek-chat")

		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, service.TkUnsupportedModelErrType, errType)
		assert.Equal(t, service.TkUnsupportedModelMessage("deepseek-chat"), msg)
		// Must NOT carry the 429 capacity backoff hint.
		assert.Empty(t, w.Header().Get("Retry-After"))
	})

	t.Run("no_available_compact_accounts_returns_429", func(t *testing.T) {
		c, _ := newCtx(t)
		status, _, _ := tkSelectFailureStatusMessage(c, service.ErrNoAvailableCompactAccounts, "gpt-5.2")

		require.Equal(t, http.StatusTooManyRequests, status)
	})

	t.Run("message_match_without_sentinel_returns_429", func(t *testing.T) {
		// Relay-hop case: the upstream TokenKey edge serialized the sentinel into a
		// plain message; the substring classifier must still catch it.
		c, _ := newCtx(t)
		status, _, _ := tkSelectFailureStatusMessage(c, errors.New("scheduler: no available openai accounts in group 7"), "gpt-5.2")

		require.Equal(t, http.StatusTooManyRequests, status)
	})

	t.Run("other_scheduler_errors_stay_503_without_retry_after", func(t *testing.T) {
		c, w := newCtx(t)
		status, errType, msg := tkSelectFailureStatusMessage(c, errors.New("pq: connection refused"), "gpt-5.2")

		require.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, "api_error", errType)
		assert.Empty(t, w.Header().Get("Retry-After"))
		assert.Equal(t, "Service temporarily unavailable", msg)
	})
}
