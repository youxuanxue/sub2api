package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Mirror-edge metric pollution (2026-06-06 yace load test): prod relays to the edge
// via cc-<edge> apikey mirror accounts; when the edge pool is empty it returns 429
// with a "No available accounts" body (tkNoAvailableAccounts, PR #575). That 429 is
// TokenKey fleet capacity, not Anthropic health, but because it carries a definitive
// upstream status the classifier counted it phase=upstream / error_owner=provider —
// so a dead single-account edge (us3: served_200=0, no_available_429=33748) and a
// healthy edge (us5: 2251x200, 77x429) BOTH read ~1300 upstream-429 on prod, making
// upstream_error_rate useless for telling a dead edge from a healthy one.
//
// These tests pin the corrected classification: a relayed downstream-capacity
// verdict ("no available accounts" / gateway failover-terminal) is owned as
// routing (phase=routing, error_owner=platform) so it stays out of
// upstream_error_rate while still counting in SLA numerator; genuine provider 429
// (rate_limit_error) and raw 5xx
// keep counting. Setups mirror the relay forward site, which records an
// appendOpsUpstreamError event carrying UpstreamStatusCode + UpstreamResponseBody.
func TestClassifyOpsDownstreamCapacityOwnedAsRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		event  *service.OpsUpstreamErrorEvent
		errMsg string
	}{
		{
			name: "edge no-available 429 body",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   429,
				Kind:                 "http_error",
				UpstreamResponseBody: `{"error":{"message":"No available accounts: no available accounts","type":"api_error"},"type":"error"}`,
			},
			errMsg: "No available accounts: no available accounts",
		},
		{
			name: "edge no-available 429 then client cancel (the polluting shape)",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   429,
				Kind:                 "request_error",
				Message:              `Post "https://api-us3.tokenkey.dev/v1/messages": context canceled`,
				UpstreamResponseBody: `{"error":{"message":"No available accounts: no available accounts","type":"api_error"}}`,
			},
			errMsg: "Upstream request failed",
		},
		{
			name: "downstream failover stopped 503",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   503,
				Kind:                 "http_error",
				UpstreamResponseBody: `{"error":{"message":"Upstream request could not be completed","type":"api_error"}}`,
			},
			errMsg: "Upstream request could not be completed",
		},
		{
			name: "legacy downstream failover message 503",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   503,
				Kind:                 "http_error",
				UpstreamResponseBody: `{"error":{"message":"All available accounts exhausted","type":"api_error"}}`,
			},
			errMsg: "All available accounts exhausted",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{tc.event})

			phase, _, errorOwner, _ := classifyOpsErrorLog(
				c, "api_error", tc.errMsg, "", http.StatusTooManyRequests)

			require.Equal(t, "routing", phase, "downstream-capacity verdict must be routing, not upstream")
			require.Equal(t, "platform", errorOwner, "must NOT be provider — otherwise it feeds upstream_error_rate")
			require.Equal(t, "platform", errorOwner, "downstream capacity is a platform routing fault in SLA numerator")
		})
	}
}

// Boundary: a genuine provider verdict carries no TokenKey envelope phrase and MUST
// keep counting toward upstream_error_rate (error_owner=provider). This is the
// anthropic_amplifier_exemption_boundary — over-broadening here would blind the
// provider-health P0.
func TestClassifyOpsGenuineUpstreamStaysProviderDespiteCapacityHelper(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		event  *service.OpsUpstreamErrorEvent
		errMsg string
		status int
	}{
		{
			name: "real Anthropic 429 rate_limit_error",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   429,
				Kind:                 "http_error",
				UpstreamResponseBody: `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`,
			},
			errMsg: "Upstream rate limit exceeded, please retry later",
			status: http.StatusTooManyRequests,
		},
		{
			name: "raw upstream 500 with no TokenKey phrase",
			event: &service.OpsUpstreamErrorEvent{
				UpstreamStatusCode:   500,
				Kind:                 "http_error",
				UpstreamResponseBody: `{"error":{"message":"internal server error","type":"server_error"}}`,
			},
			errMsg: "Upstream request failed",
			status: http.StatusBadGateway,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{tc.event})

			phase, _, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", tc.errMsg, "", tc.status)

			require.Equal(t, "upstream", phase, "genuine provider error must stay upstream")
			require.Equal(t, "provider", errorOwner, "must keep counting toward upstream_error_rate")
		})
	}
}

// Unit-level guard on the predicate itself: the status gate (0 => owned by
// client-cancel, not here) and the phrase boundary.
func TestTkUpstreamDownstreamCapacityPredicate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mk := func(status int, body, msg string) *gin.Context {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode:   status,
			UpstreamResponseBody: body,
			Message:              msg,
		}})
		return c
	}

	require.True(t, tkUpstreamDownstreamCapacity(mk(429, "No available accounts", "")))
	require.True(t, tkUpstreamDownstreamCapacity(mk(503, `{"error":{"message":"Upstream request could not be completed"}}`, "")))
	require.True(t, tkUpstreamDownstreamCapacity(mk(503, "all available accounts exhausted", "")))
	require.True(t, tkUpstreamDownstreamCapacity(mk(502, "", "No available accounts: no available accounts")))
	// status 0 (pure transport) is owned by tkUpstreamClientCanceled, not here.
	require.False(t, tkUpstreamDownstreamCapacity(mk(0, "No available accounts", "context canceled")))
	// real provider 429 — no TokenKey phrase.
	require.False(t, tkUpstreamDownstreamCapacity(mk(429, `{"type":"rate_limit_error"}`, "")))
	// nil context.
	require.False(t, tkUpstreamDownstreamCapacity(nil))
}
