//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_GrokAPIKeyRoutesToResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resp := newJSONResponse(http.StatusOK, "")
	resp.Header.Set("Content-Type", "text/event-stream")
	resp.Body = ioNopCloserString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from grok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/65/test", nil)

	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          65,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://api-us4.tokenkey.dev",
		},
	}

	err := svc.testGrokAccountConnection(c, account, "claude-sonnet-4-6", "hi")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, "https://api-us4.tokenkey.dev/v1/responses", req.URL.String())
	require.Equal(t, "Bearer edge-grok-key", req.Header.Get("Authorization"))
	require.Equal(t, defaultGrokTestModelID, gjson.GetBytes(readRequestBodyForTest(t, req), "model").String())
	body := rec.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"model":"grok-4.3"`)
	require.Contains(t, body, "hello from grok")
	require.Contains(t, body, `"type":"test_complete"`)
	require.Contains(t, body, `"success":true`)
}

func TestAccountTestService_GrokOAuthRoutesToXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resp := newJSONResponse(http.StatusOK, "")
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = ioNopCloserString(`{"status":"completed","model":"grok-code-fast-1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`)
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/6/test", nil)

	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
	}
	account := &Account{
		ID:          6,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "grok-oauth-token",
			"base_url":     "https://api.x.ai/v1",
		},
	}

	err := svc.testGrokAccountConnection(c, account, "grok-code-fast-1", "")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	req := upstream.requests[0]
	require.Equal(t, "https://api.x.ai/v1/responses", req.URL.String())
	require.Equal(t, "Bearer grok-oauth-token", req.Header.Get("Authorization"))
	require.Contains(t, rec.Body.String(), `"model":"grok-code-fast-1"`)
	require.Contains(t, rec.Body.String(), `"success":true`)
}

func TestAccountTestService_GrokRejectsDisallowedBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &queuedHTTPUpstream{}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/65/test", nil)

	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:       true,
			UpstreamHosts: []string{"api-us4.tokenkey.dev", "api.x.ai"},
		}}},
	}
	account := &Account{
		ID:          65,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "edge-grok-key",
			"base_url": "https://evil.example.com",
		},
	}

	err := svc.testGrokAccountConnection(c, account, "grok-4.3", "hi")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Invalid base URL:")
	require.Empty(t, upstream.requests, "invalid base_url must be rejected before any upstream request")
	require.Contains(t, rec.Body.String(), `"type":"error"`)
	require.Contains(t, rec.Body.String(), "Invalid base URL:")
}

func TestNormalizeGrokAdminTestModelFallsBackForNonChatModels(t *testing.T) {
	require.Equal(t, defaultGrokTestModelID, normalizeGrokAdminTestModel(""))
	require.Equal(t, defaultGrokTestModelID, normalizeGrokAdminTestModel("claude-sonnet-4-6"))
	require.Equal(t, defaultGrokTestModelID, normalizeGrokAdminTestModel("grok-imagine-image"))
	require.Equal(t, "grok-code-fast-1", normalizeGrokAdminTestModel("grok-code-fast-1"))
}

func ioNopCloserString(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
