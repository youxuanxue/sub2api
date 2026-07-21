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
)

func TestHandleKiroSilentRefusalMessagesPreservesGeneric502Contract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	handled := (&GatewayHandler{}).handleKiroSilentRefusalMessages(c, &service.KiroSilentRefusalError{}, false)

	require.True(t, handled)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, service.KiroSilentRefusalOutcome, rec.Header().Get(service.KiroOutcomeHeader))
	require.JSONEq(t, `{"type":"error","error":{"type":"upstream_error","message":"Upstream service temporarily unavailable"}}`, rec.Body.String())
}

func TestHandleKiroSilentRefusalChatCompletionsPreservesGeneric502Contract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	handled := (&GatewayHandler{}).handleKiroSilentRefusalChatCompletions(c, &service.KiroSilentRefusalError{})

	require.True(t, handled)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, service.KiroSilentRefusalOutcome, rec.Header().Get(service.KiroOutcomeHeader))
	require.JSONEq(t, `{"error":{"type":"server_error","message":"All available accounts exhausted"}}`, rec.Body.String())
}

func TestHandleKiroSilentRefusalIgnoresUnrelatedErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	require.False(t, (&GatewayHandler{}).handleKiroSilentRefusalMessages(c, errors.New("other"), false))
	require.False(t, (&GatewayHandler{}).handleKiroSilentRefusalChatCompletions(c, errors.New("other")))
	require.False(t, rec.Result().Header.Get(service.KiroOutcomeHeader) != "")
	require.Empty(t, rec.Body.String())
}
