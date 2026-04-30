//go:build unit

package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAccountTestService_NewAPI_RoutesToChatCompletions verifies that the
// fifth-platform `newapi` test connection sends the user's prompt to the
// upstream chat-completions adaptor path instead of only probing /models.
func TestAccountTestService_NewAPI_RoutesToChatCompletions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer probe-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello from upstream\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n")
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
		ChannelType: 1,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "probe-key",
			"base_url": srv.URL,
		},
	}

	err := svc.testNewAPIAccountConnection(c, account, "upstream-model-a", "hi")
	require.NoError(t, err)
	require.Equal(t, 1, hits, "must send one chat completion probe")
	body := rec.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"type":"content"`)
	require.Contains(t, body, "hello from upstream")
	require.Contains(t, body, `"type":"test_complete"`)
	require.Contains(t, body, `"success":true`)
}

// TestAccountTestService_NewAPI_ReportsUpstreamFailure verifies that an
// authentication failure or unreachable upstream surfaces a structured SSE
// error event — not a silent success.
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

	_ = svc.testNewAPIAccountConnection(c, account, "upstream-model-a", "hi")
	body := rec.Body.String()
	require.Contains(t, body, `"type":"error"`)
	require.True(t, strings.Contains(body, "bad key") || strings.Contains(body, "API returned 401"), body)
	require.NotContains(t, body, `"success":true`)
}

func TestAccountTestService_NewAPI_ReportsTruncatedChatStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)

	svc := &AccountTestService{}
	err := svc.processOpenAIChatCompletionsStream(c, strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	require.Error(t, err)

	body := rec.Body.String()
	require.Contains(t, body, "partial")
	require.Contains(t, body, `"type":"error"`)
	require.Contains(t, body, "Stream ended before chat completion finished")
	require.NotContains(t, body, `"success":true`)
}

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
		ChannelType: 0,
		Credentials: map[string]any{"api_key": "k", "base_url": "https://example.invalid"},
	}

	_ = svc.testNewAPIAccountConnection(c, account, "upstream-model-a", "hi")
	body := rec.Body.String()
	require.Contains(t, body, "channel_type")
}
