//go:build unit

package service

import (
	"context"
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

func TestSetKiroInternalThinkingMirrorHopHeaderForAccount_ExplicitEdgeBaseURL(t *testing.T) {
	hdr := http.Header{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":        "https://api-us6.tokenkey.dev",
			"mirror_platform": PlatformKiro,
		},
	}
	setKiroInternalThinkingMirrorHopHeaderForAccount(hdr, account)
	require.Equal(t, "1", hdr.Get(kiroInternalThinkingMirrorHopRequestHeader))
}

func TestSetKiroInternalThinkingMirrorHopHeaderForAccount_NativeEdgeRelayOmitsHop(t *testing.T) {
	hdr := http.Header{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us5.tokenkey.dev",
		},
	}
	setKiroInternalThinkingMirrorHopHeaderForAccount(hdr, account)
	require.Empty(t, hdr.Get(kiroInternalThinkingMirrorHopRequestHeader))
}

func TestSetKiroInternalThinkingMirrorHopHeaderForAccount_DefaultAnthropicHostOmitsRelay(t *testing.T) {
	hdr := http.Header{}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}
	setKiroInternalThinkingMirrorHopHeaderForAccount(hdr, account)
	require.Empty(t, hdr.Get(kiroInternalThinkingMirrorHopRequestHeader))
}

func TestBuildUpstreamRequest_MirrorStubSetsRelayHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{
				URLAllowlist: config.URLAllowlistConfig{Enabled: false},
			},
		},
	}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":         "edge-relay-key",
			"base_url":        "https://api-us6.tokenkey.dev",
			"mirror_platform": PlatformKiro,
		},
	}

	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-20250514","messages":[]}`),
		"edge-relay-key", "apikey", "claude-sonnet-4-20250514", true, false,
	)
	require.NoError(t, err)
	require.Equal(t, "1", getHeaderRaw(req.Header, kiroInternalThinkingMirrorHopRequestHeader))
}

func TestGatewayService_HandleStreamingResponse_StripsKiroInternalThinkingSSEComment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
		},
		rateLimitService: &RateLimitService{},
	}

	thinking := "prod mirror stub reasoning"
	payload := encodeKiroInternalThinkingPayload(kiroInternalThinkingBlocksFromPlaintext(thinking))
	commentLine := kiroInternalThinkingSSECommentPfx + payload

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`,
			"",
			commentLine,
			"",
			`data: {"type":"message_stop"}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}

	result, err := svc.handleStreamingResponse(
		context.Background(), resp, c, &Account{ID: 55},
		time.Now(), "claude-sonnet-4-20250514", "claude-sonnet-4-20250514", false,
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	out := rec.Body.String()
	require.NotContains(t, out, kiroInternalThinkingSSECommentPfx)
	require.NotContains(t, out, thinking)

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], thinking)
}
