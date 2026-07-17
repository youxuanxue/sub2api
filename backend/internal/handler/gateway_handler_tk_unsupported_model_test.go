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
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, w.Header().Get("Retry-After"))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, service.TkUnsupportedModelErrType, errObj["type"])
	assert.Equal(t, service.TkUnsupportedModelMessage("gpt"), errObj["message"])
}

func TestTkWriteUnsupportedAnthropicModelAtIngress_AllowsClaudeModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, h.tkWriteUnsupportedAnthropicModelAtIngress(c, "claude-opus-4-8", false, nil))
}
