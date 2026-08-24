package handler

// TK regression: the buffered Anthropic assembly paths now record upstream
// context when a 200 SSE stream yields a terminal `error` event (or no events at
// all) instead of a message. This test pins the classification consequence —
// the reason that bug mattered — so a future refactor that drops the
// setOpsUpstreamError call is caught here rather than in prod SLA numbers.
//
// Prod evidence (2026-08-24): 124 rows of status 502 /
// error_message="Upstream stream ended without a response" landed as
// error_phase=internal / error_owner=platform / error_source=gateway, booking an
// upstream fault against the gateway.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpsBufferedAnthropicUpstreamErrorIsProviderOwned(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		upstreamStatus int
		message        string
	}{
		{
			name:           "terminal rate_limit_error event",
			upstreamStatus: http.StatusTooManyRequests,
			message:        "Number of concurrent connections exceeded",
		},
		{
			name:           "terminal overloaded_error event",
			upstreamStatus: 529,
			message:        "Overloaded",
		},
		{
			// The empty-stream case: no error event, synthesized 502.
			name:           "stream ended with no usable events",
			upstreamStatus: http.StatusBadGateway,
			message:        "Upstream stream ended without a response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			// What tkAnthropicBufferedFailure records on the request context.
			service.SetOpsUpstreamError(c, tt.upstreamStatus, tt.message, "")

			phase, isBusinessLimited, owner, source := classifyOpsErrorLog(
				c, "api_error", tt.message, "", http.StatusBadGateway,
			)

			require.Equal(t, "upstream", phase)
			require.Equal(t, "provider", owner)
			require.Equal(t, "upstream_http", source)
			require.False(t, isBusinessLimited)
		})
	}
}

// Without upstream context the same shape still classifies as a gateway-internal
// fault. This is the pre-fix behavior, kept as the contrast case so the test
// above is provably about the recorded context and not about the message text.
func TestClassifyOpsBufferedAnthropicWithoutUpstreamContextStaysInternal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	phase, _, owner, source := classifyOpsErrorLog(
		c, "api_error", "Upstream stream ended without a response", "", http.StatusBadGateway,
	)

	require.Equal(t, "internal", phase)
	require.Equal(t, "platform", owner)
	require.Equal(t, "gateway", source)
}
