package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTkWriteDeprecatedAnthropicModelIfApplicable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	wrapped := fmt.Errorf("%w: claude-3-5-sonnet-20241022 (suggest %q)", service.ErrDeprecatedAnthropicModel, "claude-sonnet-4-6")
	require.True(t, h.tkWriteDeprecatedAnthropicModelIfApplicable(c, wrapped, "claude-3-5-sonnet-20241022", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Empty(t, w.Header().Get("Retry-After"))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	errObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, service.TkDeprecatedAnthropicErrorType, errObj["type"])
}

func TestTkWriteDeprecatedAnthropicModelIfApplicable_IgnoresOtherErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, h.tkWriteDeprecatedAnthropicModelIfApplicable(c, service.ErrNoAvailableAccounts, "claude-3-5-sonnet-20241022", nil))
}
