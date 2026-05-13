//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newGatewaySelectionTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.NoError(t, err)
	c.Request = req
	return c
}

func TestTkPrepareParsedRequestSessionInputs_PopulatesStickyAndSessionContext(t *testing.T) {
	c := newGatewaySelectionTestContext(t)
	c.Request.Header.Set("X-Session-Id", "sess-header-1")
	c.Request.Header.Set("User-Agent", "claude-code/test")

	ctx := context.WithValue(c.Request.Context(), ctxkey.RequestID, "req-1")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-1")
	c.Request = c.Request.WithContext(ctx)

	apiKey := &service.APIKey{ID: 42, GroupID: func() *int64 { v := int64(9); return &v }()}
	parsed := &service.ParsedRequest{}

	TkPrepareParsedRequestSessionInputs(c, apiKey, parsed)

	require.NotNil(t, parsed.ExplicitStickyKey)
	require.Equal(t, "sess-header-1", parsed.ExplicitStickyKey.Value)
	require.Equal(t, service.StickyKeySourceClientXSessionID, parsed.ExplicitStickyKey.Source)
	require.NotNil(t, parsed.SessionContext)
	require.Equal(t, int64(42), parsed.SessionContext.APIKeyID)
	require.Equal(t, "claude-code/test", parsed.SessionContext.UserAgent)
	require.NotNil(t, parsed.GroupID)
	require.Equal(t, int64(9), *parsed.GroupID)
	require.Equal(t, "req-1", parsed.RequestID)
	require.Equal(t, "client-1", parsed.ClientRequestID)
}

func TestTkPrepareParsedRequestSessionInputs_PrefersSessionIDHeader(t *testing.T) {
	c := newGatewaySelectionTestContext(t)
	c.Request.Header.Set("session_id", "sess-id")
	c.Request.Header.Set("conversation_id", "conv-id")
	c.Request.Header.Set("X-Claude-Code-Session-Id", "cc-id")
	c.Request.Header.Set("X-Session-Id", "x-id")

	apiKey := &service.APIKey{ID: 7}
	parsed := &service.ParsedRequest{}

	TkPrepareParsedRequestSessionInputs(c, apiKey, parsed)

	require.NotNil(t, parsed.ExplicitStickyKey)
	require.Equal(t, "sess-id", parsed.ExplicitStickyKey.Value)
	require.Equal(t, service.StickyKeySourceClientSessionID, parsed.ExplicitStickyKey.Source)
}

