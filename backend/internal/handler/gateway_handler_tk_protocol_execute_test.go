//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAttachMessagesProtocolRoutingReusesOnlyIdenticalAdmissionRequest(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	router := service.NewProtocolRouter()
	admitted, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, "claude-sonnet-4-6", false, body)
	require.NoError(t, err)

	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := service.WithProtocolRouting(context.Background(), router, admitted)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
		return c
	}
	h := &GatewayHandler{protocolRouter: router}

	t.Run("same document keeps admission request", func(t *testing.T) {
		c := newContext()
		before := c.Request.Context()
		require.NoError(t, h.tkAttachMessagesProtocolRouting(c, "claude-sonnet-4-6", false, body))
		got, ok := service.ProtocolRoutingRequest(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, admitted.Digest(), got.Digest())
		require.Equal(t, before, c.Request.Context())
	})

	t.Run("stream change reparses", func(t *testing.T) {
		c := newContext()
		require.NoError(t, h.tkAttachMessagesProtocolRouting(c, "claude-sonnet-4-6", true, body))
		got, ok := service.ProtocolRoutingRequest(c.Request.Context())
		require.True(t, ok)
		require.True(t, got.Profile().Stream)
		require.NotEqual(t, admitted.Digest(), got.Digest())
	})

	t.Run("body rewrite reparses", func(t *testing.T) {
		c := newContext()
		rewritten := []byte(`{"model":"claude-sonnet-4-6","stream":false,"messages":[{"role":"user","content":"rewritten"}]}`)
		require.NoError(t, h.tkAttachMessagesProtocolRouting(c, "claude-sonnet-4-6", false, rewritten))
		got, ok := service.ProtocolRoutingRequest(c.Request.Context())
		require.True(t, ok)
		require.True(t, got.BodyEquals(rewritten))
	})
}
