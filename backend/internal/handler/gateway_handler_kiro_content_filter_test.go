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

func TestGatewayHandler_HandleKiroContentFilteredError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h := &GatewayHandler{}

	handled := h.handleKiroContentFilteredError(c, &service.KiroContentFilteredError{})
	require.True(t, handled)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, service.KiroContentFilteredOutcome, rec.Header().Get(service.KiroOutcomeHeader))
	require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, service.KiroContentFilteredClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.True(t, service.HasOpsClientContentFiltered(c))
}

func TestGatewayHandler_HandleKiroContentFilteredError_IgnoresOtherErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	h := &GatewayHandler{}

	require.False(t, h.handleKiroContentFilteredError(c, errors.New("other")))
	require.Empty(t, rec.Body.String())
}
