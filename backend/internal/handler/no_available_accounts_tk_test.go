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
		status, msg := tkSelectFailureStatusMessage(c, service.ErrNoAvailableAccounts)

		require.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		assert.Contains(t, msg, "No available accounts")
	})

	t.Run("wrapped_no_available_accounts_returns_429", func(t *testing.T) {
		c, w := newCtx(t)
		wrapped := fmt.Errorf("%w supporting model: gpt-5.2 (channel pricing restriction)", service.ErrNoAvailableAccounts)
		status, msg := tkSelectFailureStatusMessage(c, wrapped)

		require.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		assert.Contains(t, msg, "gpt-5.2")
	})

	t.Run("no_available_compact_accounts_returns_429", func(t *testing.T) {
		c, _ := newCtx(t)
		status, _ := tkSelectFailureStatusMessage(c, service.ErrNoAvailableCompactAccounts)

		require.Equal(t, http.StatusTooManyRequests, status)
	})

	t.Run("message_match_without_sentinel_returns_429", func(t *testing.T) {
		// Relay-hop case: the upstream TokenKey edge serialized the sentinel into a
		// plain message; the substring classifier must still catch it.
		c, _ := newCtx(t)
		status, _ := tkSelectFailureStatusMessage(c, errors.New("scheduler: no available openai accounts in group 7"))

		require.Equal(t, http.StatusTooManyRequests, status)
	})

	t.Run("other_scheduler_errors_stay_503_without_retry_after", func(t *testing.T) {
		c, w := newCtx(t)
		status, msg := tkSelectFailureStatusMessage(c, errors.New("pq: connection refused"))

		require.Equal(t, http.StatusServiceUnavailable, status)
		assert.Empty(t, w.Header().Get("Retry-After"))
		assert.Equal(t, "Service temporarily unavailable", msg)
	})
}
