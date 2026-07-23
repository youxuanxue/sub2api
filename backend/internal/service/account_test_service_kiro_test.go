//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_KiroOAuthUsesKiroGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, recorder := newTestContext()

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hello from kiro","inputTokens":6,"outputTokens":3}`))
	frame = appendKiroTerminalStop(frame, "END_TURN")
	upstream := &kiroFakeUpstream{body: frame}
	account := *newKiroAccountForTest()
	account.ID = 901
	account.Name = "kiro-us5"

	svc := &AccountTestService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		kiroGatewayService: NewKiroGatewayService(upstream, nil, nil),
	}

	err := svc.TestAccountConnection(ctx, account.ID, "claude-sonnet-4-5-20250929", "hi", AccountTestModeDefault)
	require.NoError(t, err)
	require.True(t, upstream.gotRequest)

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"test_start"`)
	require.Contains(t, body, `"model":"claude-sonnet-4-5"`)
	require.Contains(t, body, "hello from kiro")
	require.Contains(t, body, `"type":"test_complete"`)
	require.Contains(t, body, `"success":true`)
}

func TestAccountTestService_KiroMirrorStubNormalizesDatedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, recorder := newTestContext()

	resp := newJSONResponse(http.StatusOK, "")
	resp.Body = io.NopCloser(strings.NewReader(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}

data: {"type":"message_stop"}

`))
	upstream := &queuedHTTPUpstream{responses: []*http.Response{resp}}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := &Account{
		ID:          902,
		Name:        "kiro-us5",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":         "edge-key",
			"base_url":        "https://api-us5.tokenkey.dev",
			"mirror_platform": "kiro",
			"model_mapping": map[string]any{
				"claude-sonnet-4-5-20250929": "claude-sonnet-4-5-20250929",
			},
		},
	}

	err := svc.testClaudeAccountConnection(ctx, account, "claude-sonnet-4-5-20250929")
	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	bodyBytes, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", gjson.GetBytes(bodyBytes, "model").String())
	require.Contains(t, recorder.Body.String(), `"model":"claude-sonnet-4-5"`)
}

func TestNormalizeKiroAdminTestModel(t *testing.T) {
	require.Equal(t, KiroDefaultTestModel, normalizeKiroAdminTestModel(""))
	require.Equal(t, "claude-sonnet-4-5", normalizeKiroAdminTestModel("claude-sonnet-4-5-20250929"))
	require.Equal(t, "claude-opus-4-5", normalizeKiroAdminTestModel("claude-opus-4-5-20251101"))
	require.Equal(t, "claude-opus-4-8", normalizeKiroAdminTestModel("claude-opus-4-8"))

	payload, err := createKiroAdminTestPayload("claude-sonnet-4-5", " hi ")
	require.NoError(t, err)
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(encoded, "stream").Bool())
	require.Equal(t, "hi", gjson.GetBytes(encoded, "messages.0.content.0.text").String())
}
