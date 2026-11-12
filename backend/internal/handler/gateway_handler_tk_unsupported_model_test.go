package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTkWriteUnsupportedAnthropicModelAtIngress(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	require.True(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, "gpt", false, nil))
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Empty(t, w.Header().Get("Retry-After"))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	// Official Anthropic 404 shape (docs/approved/unsupported-model-404-ssot.md).
	assert.Equal(t, "error", payload["type"])
	assert.Equal(t, "not_found_error", errObj["type"])
	assert.Equal(t, service.TkUnsupportedModelMessage("gpt"), errObj["message"])
}

func TestTkWriteUnsupportedAnthropicModelAtIngress_AllowsClaudeModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, "claude-opus-4-8", false, nil))
}

func TestTkWriteUnsupportedAnthropicModelAtIngress_AllowsCloudwiseWhitelistModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	for _, model := range []string{"glm-5.2", "glm-5.3", "kimi-k3", "MiniMax-M3", "deepseek-v4-pro"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.False(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, model, false, nil), model)
	}
}

func TestTkWriteUnsupportedAnthropicModelAtIngress_RejectsFormerTokenseaFloorModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	for _, model := range []string{"gpt-5.5", "gpt-image-2.5"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		require.True(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, model, false, nil), model)
		require.Equal(t, http.StatusNotFound, w.Code)
	}
	for _, model := range []string{"gemini-3-pro-image", "nano-banana-2"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.False(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, model, false, nil), model)
	}
}

func TestTkWriteUnsupportedAnthropicModelAtIngress_RejectsTokenseaListedButUnservedQwen(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	// qwen3.7-max is listed on tokensea GET /v1/models but raw chat 400; it is
	// also outside CloudWise prefixes, so Anthropic ingress must reject it (404 SSOT).
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.True(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, "qwen3.7-max", false, nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}
