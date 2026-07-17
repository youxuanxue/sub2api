package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Issue #625 (prod + us2/us3/uk1 quad P0 2026-06-06T07:00-07:16Z): a single client
// issuing non-streaming claude-opus-4-6 with a short client timeout cancels
// mid-flight. The outbound request fails with `context canceled` and NO upstream
// HTTP status (upstream_status_code=null, final 502), so the classifier relabeled
// it phase=upstream / error_owner=provider and it counted toward upstream_error_rate.
// Because prod relays the same request to edges, one client cancel lit up prod AND
// every relayed edge at once.
//
// These tests pin the corrected classification: a client-cancel upstream transport
// error is owned by the client (phase=request, error_owner=client) so it drops out
// of the upstream_excl filter behind upstream_error_rate, while server-side
// deadline-exceeded timeouts and genuine provider failures keep counting.
//
// Setups mirror the gateway forward-failure site (gateway_service.go), which records
// BOTH setOpsUpstreamError(c, 0, safeErr, "") and an appendOpsUpstreamError event
// with UpstreamStatusCode=0, Kind="request_error", Message=safeErr — where safeErr
// is the sanitized Go error (sanitizeUpstreamErrorMessage preserves "context
// canceled"). hasOpsUpstreamErrorContext keys off the events array, so the event is
// what makes upstreamError=true.
func TestClassifyOpsUpstreamClientCanceledOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("context canceled transport error (status 0) via upstream event message", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            `Post "https://api.anthropic.com/v1/messages?beta=true": context canceled`,
		}})

		phase, errorOwner, errorSource := classifyOpsErrorLog(
			c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it feeds upstream_error_rate")
		require.Equal(t, "client_request", errorSource)
	})

	t.Run("inbound request context canceled (event message lacks cancel signature) via Request.Context", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", nil)
		c.Request = req
		// Forward-failure event exists (status 0) but its message does not carry the
		// cancel signature — Request.Context().Err()==context.Canceled is the signal.
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            "Upstream request failed",
		}})

		phase, errorOwner, _ := classifyOpsErrorLog(
			c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})
}

// A server/upstream timeout surfaces as context.DeadlineExceeded ("deadline
// exceeded") and is genuine evidence of upstream slowness — it MUST stay
// provider-owned and keep counting toward upstream_error_rate.
func TestClassifyOpsUpstreamDeadlineExceededStaysProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		message string
	}{
		{"context deadline exceeded", `Post "https://api.anthropic.com/v1/messages": context deadline exceeded`},
		{"i/o timeout", `dial tcp: i/o timeout`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
				UpstreamStatusCode: 0,
				Kind:               "request_error",
				Message:            tc.message,
			}})

			phase, errorOwner, _ := classifyOpsErrorLog(
				c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

			require.Equal(t, "upstream", phase, "server-side timeout is genuine upstream evidence")
			require.Equal(t, "provider", errorOwner, "must keep counting toward upstream_error_rate")
		})
	}
}

// A genuine upstream 5xx WITH a status code must never be misread as a client
// cancel even if the connection later dropped — the status gate keeps it provider.
func TestClassifyOpsUpstream5xxWithStatusStaysProviderNotCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	service.SetOpsUpstreamError(c, http.StatusInternalServerError, "internal server error", "")

	phase, errorOwner, _ := classifyOpsErrorLog(
		c, "upstream_error", "Upstream service temporarily unavailable", "", http.StatusBadGateway)

	require.Equal(t, "upstream", phase)
	require.Equal(t, "provider", errorOwner)
}
