//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKiroLocal413DiagnosticsSurviveOpsMiddleware(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	t.Cleanup(func() { resetOpsErrorLoggerStateForTest(t) })
	gin.SetMode(gin.TestMode)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h := &GatewayHandler{}
	const detail = `{"kind":"kiro_local_payload_too_large","converter_input_bytes":1000000}`
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		c.Set(service.OpsInternalErrorDetailKey, detail)
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, &service.KiroInvalidRequestError{
			StatusCode:      http.StatusRequestEntityTooLarge,
			Message:         "Request is too large for Kiro after conversation history truncation",
			ContextOverflow: true,
		}))
	})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	// The local guard ships 400 invalid_request_error so Anthropic-shaped
	// clients read it as a prompt-length problem; ops still gets the full
	// size diagnostics, which is what this test exists to protect.
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, service.TkClientFacingContextOverflowMessage, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.NotContains(t, rec.Body.String(), "converter_input_bytes")
	select {
	case job := <-opsErrorLogQueue:
		require.Equal(t, detail, *job.entry.UpstreamErrorDetail)
		require.Equal(t, "client_request", job.entry.ErrorSource)
		require.Equal(t, "client", job.entry.ErrorOwner)
		require.Equal(t, http.StatusBadRequest, job.entry.StatusCode)
		require.False(t, job.entry.Stream)
		require.Nil(t, job.entry.UpstreamStatusCode)
		require.Empty(t, job.entry.UpstreamErrors)
	default:
		t.Fatal("local 413 must enqueue its size diagnostics")
	}
}

// Companion extraction of Messages-path non-failover errors must preserve the
// same status codes / no-failover early returns as the prior inline chain.
func TestTkHandleMessagesNonFailoverForwardError_Matrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}

	t.Run("content_filtered", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, &service.KiroContentFilteredError{}))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	})

	t.Run("invalid_model", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroInvalidModelError{Model: "no-such-model"}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Equal(t, err.ClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	})

	t.Run("invalid_request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroInvalidRequestError{Message: "bad tools"}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, err.ClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	})

	// Local input-limit guard: 400 invalid_request_error with actionable copy.
	t.Run("context_overflow", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroInvalidRequestError{
			StatusCode:      http.StatusRequestEntityTooLarge,
			Message:         "Request is too large for Kiro after conversation history truncation",
			ContextOverflow: true,
		}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Equal(t, service.TkClientFacingContextOverflowMessage, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	})

	// An upstream byte-ceiling 413 (no ContextOverflow) keeps 413 and the bare
	// SSOT copy: that is a different failure the caller cannot fix by compacting.
	t.Run("request_too_large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroInvalidRequestError{
			StatusCode: http.StatusRequestEntityTooLarge,
			Message:    "request body exceeds the maximum allowed size",
		}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		require.Equal(t, "request_too_large", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Equal(t, service.TkClientFacingRequestTooLargeMessage, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	})

	t.Run("quota_exhausted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroEndpointQuotaExhaustedError{}
		require.False(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Empty(t, rec.Body.String())
		require.Equal(t, 200, rec.Code)
	})

	t.Run("canonical_ua_reject", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.CanonicalIngressUARejectedError{IngressUA: "curl/8"}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "permission_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.True(t, service.HasOpsClientPolicyDenied(c))
	})

	t.Run("other_error_not_handled", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		require.False(t, h.tkHandleMessagesNonFailoverForwardError(c, errors.New("transport timeout")))
		require.Empty(t, rec.Body.String())
		require.Equal(t, 200, rec.Code) // gin default; handler must not write
	})
}
