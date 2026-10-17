//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestOpenAIResponseProtectionVerdictIsProviderOwned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
		UpstreamStatusCode: http.StatusBadGateway,
		Platform:           service.PlatformOpenAI,
		AccountID:          19,
		Kind:               "stream_failed",
		Message:            "response protection is unavailable",
	}})

	phase, isBusinessLimited, errorOwner, errorSource := classifyOpsErrorLog(
		c, "api_error", "response protection is unavailable", "upstream_error", http.StatusBadGateway)

	require.Equal(t, "upstream", phase,
		"must not be internal — internal + platform + 5xx is daily_error_report's code_owned gate")
	require.Equal(t, "provider", errorOwner)
	require.Equal(t, "upstream_http", errorSource)
	require.False(t, isBusinessLimited)
}

func TestOpsErrorLoggerMiddleware_StreamFailureResponseProtectionIsProviderOwned(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		// Reproduce the pre-fix shape: wire 200 + terminal SSE, no ops
		// upstream event on the gin context.
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("event: response.failed\n")
		_, _ = c.Writer.WriteString(`data: {"type":"response.failed","response":{"status":"failed","output":[],"error":{"type":"internal_error","code":"upstream_error","message":"response protection is unavailable"}}}`)
		_, _ = c.Writer.WriteString("\n\n")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, http.StatusBadGateway, job.entry.StatusCode)
	require.Equal(t, "api_error", job.entry.ErrorType)
	require.Equal(t, "response protection is unavailable", job.entry.ErrorMessage)
	require.Equal(t, "upstream", job.entry.ErrorPhase,
		"StreamFailure must not leave platform/internal after fabricating upstream_*")
	require.Equal(t, "provider", job.entry.ErrorOwner)
	require.Equal(t, "upstream_http", job.entry.ErrorSource)
	require.NotNil(t, job.entry.UpstreamStatusCode)
	require.Equal(t, http.StatusBadGateway, *job.entry.UpstreamStatusCode)
	require.NotNil(t, job.entry.UpstreamErrorMessage)
	require.Equal(t, "response protection is unavailable", *job.entry.UpstreamErrorMessage)
}

func TestOpsErrorLoggerMiddleware_StreamFailureClientFaultStaysClientOwned(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/responses", func(c *gin.Context) {
		c.Status(http.StatusOK)
		_, _ = c.Writer.WriteString("event: response.failed\n")
		_, _ = c.Writer.WriteString(`data: {"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"input exceeds the context window"}}}`)
		_, _ = c.Writer.WriteString("\n\n")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))

	require.Equal(t, int64(1), OpsErrorLogQueueLength())
	job := <-opsErrorLogQueue
	require.Equal(t, http.StatusBadRequest, job.entry.StatusCode)
	require.Equal(t, "invalid_request_error", job.entry.ErrorType)
	require.Equal(t, "request", job.entry.ErrorPhase)
	require.Equal(t, "client", job.entry.ErrorOwner)
}
