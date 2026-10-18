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

	t.Run("upstream gateway pool-empty envelope stays routing-owned", func(t *testing.T) {
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
