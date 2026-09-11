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

// Prod + edge-us4-ls daily error ledger, 2026-09-10: Antigravity
// gemini-2.5-flash-image 503 (4→22) and claude-opus-4-6 502 (2→7) were reported
// as owner=platform / phase=internal. daily_error_report.py derives
// `code_owned = owner == "platform" and phase == "internal" and status >= 500`,
// so both clusters became confidence=high, repair_eligible=true, and were
// queued for an automated code-repair Draft PR — against code that is not at
// fault. Meanwhile the genuine provider signal stayed out of
// upstream_error_rate.
//
// The cause was upstream of classification: handleSmartRetry's terminal exits
// recorded no ops upstream event, so hasOpsUpstreamErrorContext was false and
// api_error defaulted to phase="internal" => owner="platform".
//
// service/antigravity_gateway_tk_smart_retry_attribution.go now records the
// verdict. These tests pin the resulting classification, and that the existing
// TK narrowings still take precedence over it.
func TestAntigravitySmartRetryVerdictIsProviderOwned(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name   string
		status int
		kind   string
		model  string
	}{
		{"capacity exhausted 503 (gemini image)", http.StatusServiceUnavailable, "smart_retry_capacity_exhausted", "gemini-2.5-flash-image"},
		{"rate limited switch 502 (opus)", http.StatusBadGateway, "smart_retry_rate_limited", "claude-opus-4-6"},
		{"single account backoff exhausted 503", http.StatusServiceUnavailable, "single_account_retry_exhausted", "gemini-3.1-flash-image"},
		{"capacity dedup 503", http.StatusServiceUnavailable, "smart_retry_capacity_dedup", "gemini-2.5-flash-image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
				UpstreamStatusCode: tc.status,
				Platform:           service.PlatformAntigravity,
				AccountID:          11,
				Kind:               tc.kind,
				Message:            "The model is overloaded. Please try again later.",
			}})

			// The gateway's terminal client envelope for a failover-exhausted
			// request is api_error, which is what previously defaulted to
			// phase=internal.
			phase, _, errorOwner, errorSource := classifyOpsErrorLog(
				c, "api_error", service.GatewayFailoverClientMessage(tc.status), "", tc.status)

			require.Equal(t, "upstream", phase,
				"must not be internal — internal + platform + 5xx is exactly daily_error_report's code_owned gate")
			require.Equal(t, "provider", errorOwner,
				"provider capacity exhaustion must not be booked as a TokenKey code defect")
			require.Equal(t, "upstream_http", errorSource)
		})
	}
}

// TestAntigravitySmartRetryAttributionKeepsTKNarrowings pins that restoring
// provider attribution does not regress the two established TK narrowings,
// which both read the same captured verdict.
func TestAntigravitySmartRetryAttributionKeepsTKNarrowings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("relayed edge pool-empty envelope stays routing-owned", func(t *testing.T) {
		// A prod→edge mirror account answering with TokenKey's own pool-empty
		// envelope is our fleet capacity, not provider health
		// (tkUpstreamDownstreamCapacity).
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusServiceUnavailable,
			Platform:           service.PlatformAntigravity,
			AccountID:          12,
			Kind:               "smart_retry_exhausted",
			Message:            "No available accounts",
		}})

		phase, _, errorOwner, _ := classifyOpsErrorLog(
			c, "api_error", "No available accounts", "", http.StatusServiceUnavailable)

		require.Equal(t, "routing", phase)
		require.Equal(t, "platform", errorOwner,
			"our own empty pool is platform-owned routing, not provider health")
	})

	t.Run("client cancel during smart retry stays client-owned", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: 0,
			Platform:           service.PlatformAntigravity,
			AccountID:          13,
			Kind:               "request_error",
			Message:            `Post "https://ag-1.test/v1internal:streamGenerateContent": context canceled`,
		}})

		phase, _, errorOwner, _ := classifyOpsErrorLog(
			c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})
}
