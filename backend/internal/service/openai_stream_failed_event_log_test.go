//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLogOpenAIStreamFailedEvent_FailoverCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(OpsModelKey, "gpt-5.5")
	account := &Account{ID: 73, Name: "GPT-pro3", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"server_error","code":"model_capacity","message":"Selected model is at capacity. Please try a different model."}}}`)

	logOpenAIStreamFailedEvent(context.Background(), c, account, "rid-cap-1", payload, "Selected model is at capacity. Please try a different model.", false, false)

	require.True(t, logSink.ContainsMessageAtLevel("openai.stream_failed_event.failover_candidate", "info"))
	require.True(t, logSink.ContainsFieldValue("failover_eligible", "true"))
	require.True(t, logSink.ContainsFieldValue("client_output_started", "false"))
	require.True(t, logSink.ContainsFieldValue("error_code", "model_capacity"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.5"))
}

func TestLogOpenAIStreamFailedEvent_PostOutputCapacityWarnsAndMarksCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Set(OpsModelKey, "gpt-5.5")
	account := &Account{ID: 73, Name: "GPT-pro3", Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"server_error","code":"model_capacity","message":"Selected model is at capacity. Please try a different model."}}}`)

	logOpenAIStreamFailedEvent(context.Background(), c, account, "rid-cap-2", payload, "Selected model is at capacity. Please try a different model.", true, true)

	require.True(t, logSink.ContainsMessageAtLevel("openai.stream_failed_event.forwarded_to_client", "warn"))
	require.True(t, logSink.ContainsFieldValue("failover_possible", "false"))
	require.True(t, logSink.ContainsFieldValue("remote_compact", "true"))
	require.True(t, logSink.ContainsFieldValue("passthrough_mode", "true"))
	require.True(t, logSink.ContainsFieldValue("error_code", "model_capacity"))
}
