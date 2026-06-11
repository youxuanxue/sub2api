package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// PR #691 canary prep: when a strict-mode edge rejects a non-CC client with the
// canonical-ingress 403, the prod mirror stub sees it as an upstream 403. It is
// the END CLIENT's identity problem — it must be client-owned (out of
// upstream_error_rate / provider-health P0), while genuine Anthropic 403s stay
// provider-owned.
func TestClassifyOpsRelayedCanonicalIngressReject403OwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("relayed strict-403 from edge is client-owned", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		edgeMsg := (&service.CanonicalIngressUARejectedError{IngressUA: "python-requests/2.31"}).Error()
		service.SetOpsUpstreamError(c, http.StatusForbidden, edgeMsg,
			`{"type":"error","error":{"type":"permission_error","message":"`+edgeMsg+`"}}`)

		phase, _, errorOwner, _ := classifyOpsErrorLog(c, "permission_error", "Upstream request failed", "", http.StatusForbidden)

		require.Equal(t, "request", phase, "relayed strict-403 must be client-owned, out of upstream_error_rate")
		require.Equal(t, "client", errorOwner)
	})

	t.Run("genuine anthropic 403 stays provider-owned", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusForbidden,
			"This organization has been disabled.",
			`{"type":"error","error":{"type":"permission_error","message":"This organization has been disabled."}}`)

		phase, _, errorOwner, _ := classifyOpsErrorLog(c, "permission_error", "Upstream request failed", "", http.StatusForbidden)

		require.Equal(t, "upstream", phase)
		require.Equal(t, "provider", errorOwner)
	})
}
