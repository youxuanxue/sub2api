//go:build unit

package service

import (
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeAccountConnection_APIKeyOmitsClaudeCodeIdentity(t *testing.T) {
	const apiKey = "sk-ant-probe"
	req := captureClaudeAccountProbe(t, &Account{
		ID:          8525,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": apiKey},
	}, http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")

	body := readProbeBody(t, req)
	require.Equal(t, claude.DefaultTestModel, gjson.GetBytes(body, "model").String())
	require.Equal(t, "user", gjson.GetBytes(body, "messages.0.role").String())
	require.Equal(t, "hi", gjson.GetBytes(body, "messages.0.content").String())
	require.Equal(t, int64(1), gjson.GetBytes(body, "messages.#").Int())
	require.Equal(t, int64(1024), gjson.GetBytes(body, "max_tokens").Int())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.False(t, gjson.GetBytes(body, "system").Exists())
	require.False(t, gjson.GetBytes(body, "metadata").Exists())
	require.False(t, gjson.GetBytes(body, "temperature").Exists())
	require.NotContains(t, string(body), "cache_control")

	for key := range claude.DefaultHeaders() {
		require.Empty(t, req.Header.Get(key), key)
	}
	require.Equal(t, apiKey, req.Header.Get("x-api-key"))
	require.Empty(t, req.Header.Get("Authorization"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	require.Empty(t, getHeaderRaw(req.Header, "anthropic-beta"))
	require.Equal(t, testClaudeAPIURL, req.URL.String())
}

func TestClaudeAccountConnection_APIKeyHeaderOverrideSetsBeta(t *testing.T) {
	const beta = "interleaved-thinking-2025-05-14"
	req := captureClaudeAccountProbe(t, &Account{
		ID:          8526,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                 "sk-ant-override",
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"anthropic-beta": beta,
			},
		},
	}, http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")

	require.Equal(t, beta, getHeaderRaw(req.Header, "anthropic-beta"))
	for key := range claude.DefaultHeaders() {
		require.Empty(t, req.Header.Get(key), key)
	}
	require.Equal(t, "sk-ant-override", req.Header.Get("x-api-key"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
}

func TestClaudeAccountConnection_APIKeyCreditErrorStaysFailure(t *testing.T) {
	ginCtx, recorder := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(
		http.StatusBadRequest,
		`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`,
	)}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{
		ID:          8527,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-ant-empty"},
	}

	err := svc.testClaudeAccountConnection(ginCtx, account, claude.DefaultTestModel)
	require.Error(t, err)
	require.Contains(t, err.Error(), "API returned 400")
	require.Contains(t, err.Error(), "credit balance is too low")
	require.NotContains(t, recorder.Body.String(), `"success":true`)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)

	require.Len(t, upstream.requests, 1)
	body := readProbeBody(t, upstream.requests[0])
	require.False(t, gjson.GetBytes(body, "system").Exists())
	require.Equal(t, "hi", gjson.GetBytes(body, "messages.0.content").String())
}

func TestClaudeAccountConnection_OAuthKeepsClaudeCodeIdentity(t *testing.T) {
	const token = "oauth-probe-token"
	req := captureClaudeAccountProbe(t, &Account{
		ID:          8528,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": token},
	}, http.StatusOK, "data: {\"type\":\"message_stop\"}\n\n")

	body := readProbeBody(t, req)
	require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(body, "system.0.text").String())
	require.NotEmpty(t, gjson.GetBytes(body, "metadata.user_id").String())
	require.Equal(t, "ephemeral", gjson.GetBytes(body, "messages.0.content.0.cache_control.type").String())
	require.Equal(t, "hi", gjson.GetBytes(body, "messages.0.content.0.text").String())

	for key, value := range claude.DefaultHeaders() {
		require.Equal(t, value, req.Header.Get(key), key)
	}
	require.Equal(t, "Bearer "+token, req.Header.Get("Authorization"))
	require.Equal(t, claude.DefaultBetaHeader, req.Header.Get("anthropic-beta"))
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Empty(t, req.Header.Get("x-api-key"))
}

func captureClaudeAccountProbe(t *testing.T, account *Account, status int, responseBody string) *http.Request {
	t.Helper()
	ginCtx, recorder := newTestContext()
	upstream := &queuedHTTPUpstream{responses: []*http.Response{newJSONResponse(status, responseBody)}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}

	err := svc.testClaudeAccountConnection(ginCtx, account, claude.DefaultTestModel)
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	require.Len(t, upstream.requests, 1)
	return upstream.requests[0]
}

func readProbeBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return body
}
