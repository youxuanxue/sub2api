package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGatewayService_BuildAnthropicVertexServiceAccountRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Authorization", "Bearer inbound-token")
	c.Request.Header.Set("X-Api-Key", "inbound-api-key")
	c.Request.Header.Set("Anthropic-Version", "2023-06-01")
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{
		ID:       301,
		Platform: PlatformAnthropic,
		Type:     AccountTypeServiceAccount,
		Credentials: map[string]any{
			"project_id": "vertex-proj",
			"location":   "us-east5",
		},
	}
	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)

	svc := &GatewayService{}
	req, err := svc.buildUpstreamRequest(
		context.Background(),
		c,
		account,
		body,
		"vertex-token",
		"service_account",
		"claude-sonnet-4-5@20250929",
		false,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "https://us-east5-aiplatform.googleapis.com/v1/projects/vertex-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-5@20250929:rawPredict", req.URL.String())
	require.Equal(t, "Bearer vertex-token", getHeaderRaw(req.Header, "authorization"))
	require.Empty(t, getHeaderRaw(req.Header, "x-api-key"))
	require.Empty(t, getHeaderRaw(req.Header, "anthropic-version"))
	require.Equal(t, "interleaved-thinking-2025-05-14", getHeaderRaw(req.Header, "anthropic-beta"))

	got := readRequestBodyForTest(t, req)
	require.Equal(t, "", gjson.GetBytes(got, "model").String())
	require.Equal(t, vertexAnthropicVersion, gjson.GetBytes(got, "anthropic_version").String())
	require.Equal(t, "hello", gjson.GetBytes(got, "messages.0.content").String())
}

// TestGatewayService_VertexAnthropicPreservesClaudeCodeSessionID asserts that the
// Vertex-Anthropic outbound builder participates in the multi-hop sticky-session
// preservation chain (commit 42f79b3f). Without this, prod→sibling-gateway and
// edge hops over Vertex Anthropic would lose X-Claude-Code-Session-Id, scattering
// sticky bindings on the downstream node.
func TestGatewayService_VertexAnthropicPreservesClaudeCodeSessionID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Version", "2023-06-01")

	account := &Account{
		ID:       302,
		Platform: PlatformAnthropic,
		Type:     AccountTypeServiceAccount,
		Credentials: map[string]any{
			"project_id": "vertex-proj",
			"location":   "us-east5",
		},
	}
	deviceID := strings.Repeat("a", 64)
	uid := FormatMetadataUserID(deviceID, "", "22222222-2222-2222-2222-222222222222", "")
	body := []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":32,"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"` + uid + `"}}`)

	svc := &GatewayService{}
	req, err := svc.buildUpstreamRequest(
		context.Background(),
		c,
		account,
		body,
		"vertex-token",
		"service_account",
		"claude-sonnet-4-5@20250929",
		false,
		false,
	)
	require.NoError(t, err)
	require.Equal(t, "22222222-2222-2222-2222-222222222222", getHeaderRaw(req.Header, "X-Claude-Code-Session-Id"))
}

func readRequestBodyForTest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.Body)
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return body
}
