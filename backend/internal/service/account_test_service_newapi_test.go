//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAccountTestService_NewAPI_RoutesToUpstreamModelsProbe verifies that the
// fifth-platform `newapi` test connection no longer falls through to the
// Claude/Anthropic path (which would have sent claude.DefaultHeaders to an
// arbitrary OpenAI-compat upstream). It must call the upstream
// GET /v1/models endpoint and report success when the upstream returns 200.
func TestAccountTestService_NewAPI_RoutesToUpstreamModelsProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		require.Equal(t, http.MethodGet, r.Method, "newapi probe must be GET, claude/anthropic path would POST")
		require.Equal(t, "/v1/models", r.URL.Path, "newapi probe must hit /v1/models, not /v1/messages")
		require.Equal(t, "Bearer probe-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"upstream-model-a"},{"id":"upstream-model-b"}]}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	svc := &AccountTestService{}
	account := &Account{
		ID:          901,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1, // OpenAI-compat default
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "probe-key",
			"base_url": srv.URL,
		},
	}

	err := svc.testNewAPIAccountConnection(c, account)
	require.NoError(t, err)
	require.Equal(t, 1, hits, "must probe upstream exactly once")
	body := rec.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"type":"test_end"`)
	require.Contains(t, body, `"success":true`)
	require.Contains(t, body, "upstream-model-a")
}

// TestAccountTestService_NewAPI_ReportsUpstreamFailure verifies that an
// authentication failure or unreachable upstream surfaces a structured SSE
// error event (non-zero exit at the handler level) — not a silent success.
func TestAccountTestService_NewAPI_ReportsUpstreamFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	svc := &AccountTestService{}
	account := &Account{
		ID:          902,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "bad-key",
			"base_url": srv.URL,
		},
	}

	_ = svc.testNewAPIAccountConnection(c, account)
	body := rec.Body.String()
	// sendErrorAndEnd writes a single SSE event with type=error carrying the
	// upstream message; surface upstream auth failure rather than silent success.
	require.Contains(t, body, `"type":"error"`)
	require.Contains(t, body, "Upstream probe failed")
	require.NotContains(t, body, `"success":true`)
}

// TestAccountTestService_NewAPI_RejectsMissingChannelType ensures that a
// newapi account created without a channel_type (mis-configuration) gets a
// clear admin-facing error instead of a misleading "Claude" probe.
func TestAccountTestService_NewAPI_RejectsMissingChannelType(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	svc := &AccountTestService{}
	account := &Account{
		ID:          903,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 0, // missing
		Credentials: map[string]any{"api_key": "k", "base_url": "https://example.invalid"},
	}

	_ = svc.testNewAPIAccountConnection(c, account)
	body := rec.Body.String()
	require.Contains(t, body, "channel_type")
}
