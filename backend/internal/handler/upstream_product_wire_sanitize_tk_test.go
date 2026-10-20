package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTkSanitizeUpstreamProductWireMessage_RewritesAgentPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	got := tkSanitizeUpstreamProductWireMessage(
		c,
		http.StatusNotFound,
		"not_found_error",
		"The requested model does not support the agent plan feature. Please refer to https://www.volcengine.com/docs/82379/2366394",
	)
	require.Equal(t, service.TkClientFacingUnsupportedMessage, got)
}

func TestWriteOpenAIUpstreamClientError_StripsAgentPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte(`{"error":{"type":"not_found_error","message":"The requested model does not support the agent plan feature."}}`)
	service.WriteOpenAIUpstreamClientError(
		c,
		http.StatusNotFound,
		body,
		"The requested model does not support the agent plan feature. Please refer to https://www.volcengine.com/docs/82379/2366394",
	)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, service.TkClientFacingUnsupportedMessage, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
}
