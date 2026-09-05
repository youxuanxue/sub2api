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
		require.Equal(t, service.KiroContentFilteredOutcome, rec.Header().Get(service.KiroOutcomeHeader))
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

	t.Run("quota_exhausted", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		err := &service.KiroEndpointQuotaExhaustedError{}
		require.True(t, h.tkHandleMessagesNonFailoverForwardError(c, err))
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		require.Equal(t, "rate_limit_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.NotEmpty(t, rec.Header().Get("Retry-After"))
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
