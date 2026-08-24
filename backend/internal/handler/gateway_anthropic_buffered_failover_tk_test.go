//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUS047_GatewayFailoverExhaustedPreservesClientErrorType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	(&GatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:       http.StatusOK,
		ResponseBody:     []byte(`{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`),
		ClientStatusCode: http.StatusNotFound,
		ClientErrorType:  "not_found_error",
		ClientMessage:    "model not found",
	}, service.PlatformAnthropic, false)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "not_found_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "model not found", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestUS047_ResponsesFailoverExhaustedPreservesClientErrorType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	(&GatewayHandler{}).handleResponsesFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:       http.StatusOK,
		ClientStatusCode: http.StatusRequestEntityTooLarge,
		ClientErrorType:  "request_too_large",
		ClientMessage:    "request is too large",
	}, false)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, "request_too_large", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Equal(t, "request is too large", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestUS047_OpenAIFailoverExhaustedPreservesClientErrorType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	(&OpenAIGatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:       http.StatusOK,
		ResponseBody:     []byte(`{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`),
		ClientStatusCode: http.StatusNotFound,
		ClientErrorType:  "not_found_error",
		ClientMessage:    "model not found",
	}, false)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "not_found_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "model not found", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestUS047_EmptyClientErrorTypeKeepsLegacyDefaults(t *testing.T) {
	t.Run("messages", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

		(&GatewayHandler{}).handleFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:       http.StatusServiceUnavailable,
			ClientStatusCode: http.StatusBadRequest,
			ClientMessage:    "legacy client error",
		}, service.PlatformAnthropic, false)

		require.Equal(t, "api_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	})

	t.Run("responses", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		(&GatewayHandler{}).handleResponsesFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode:       http.StatusServiceUnavailable,
			ClientStatusCode: http.StatusBadRequest,
			ClientMessage:    "legacy client error",
		}, false)

		require.Equal(t, "server_error", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	})
}
