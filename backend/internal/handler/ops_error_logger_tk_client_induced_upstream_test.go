package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Prod P0 2026-06-05T14:21Z: a client looping models the Codex / ChatGPT-OAuth
// backend cannot serve drove upstream_error_rate to 40.32% (overall) because the
// upstream 400 invalid_request_error rejections were classified as
// error_owner=provider and counted as upstream/provider health failures.
//
// These tests pin the corrected classification: client-induced upstream 4xx are
// owned by the client (phase=request, error_owner=client) so they drop out of the
// upstream_excl filter behind upstream_error_rate, while genuine provider failures
// and account-level 4xx keep counting.
func TestClassifyOpsUpstreamClientInducedRejectionOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("openai chat/completions unsupported-model 400 (structured invalid_request_error body)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode:   http.StatusBadRequest,
			UpstreamResponseBody: `{"error":{"message":"The 'gpt-5.4-nano' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}}`,
		}})

		errType := normalizeOpsErrorType("api_error", "")
		phase, isBusinessLimited, errorOwner, errorSource := classifyOpsErrorLog(c, errType, "Upstream request failed", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it feeds upstream_error_rate")
		require.Equal(t, "client_request", errorSource)
		require.False(t, isBusinessLimited)
	})

	t.Run("openai /v1/responses unsupported-model surfaced as wrapped upstream_error (msg-only signal)", func(t *testing.T) {
		// On /v1/responses the surfaced envelope type is upstream_error and the
		// final status is 502; the only client-induced signal is the upstream
		// message. The upstream status on the context is still 400.
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusBadRequest,
			"The 'codex-mini-latest' model is not supported when using Codex with a ChatGPT account.", "")

		phase, _, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})

	t.Run("upstream 413 request_too_large is always client-induced", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusRequestEntityTooLarge, "request too large", "")

		phase, _, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "request too large", "", http.StatusRequestEntityTooLarge)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})

	t.Run("upstream 400 invalid_request_error via message substring", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusBadRequest,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`, "")

		phase, _, errorOwner, _ := classifyOpsErrorLog(c, "api_error", "invalid request", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})
}

func TestClassifyOpsGenuineUpstreamErrorsStayProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		status  int
		message string
		errType string
		final   int
	}{
		{"upstream 500 internal", http.StatusInternalServerError, "internal server error", "upstream_error", http.StatusBadGateway},
		{"upstream 401 account auth", http.StatusUnauthorized, "unauthorized", "authentication_error", http.StatusUnauthorized},
		{"upstream 403 forbidden", http.StatusForbidden, "forbidden", "upstream_error", http.StatusForbidden},
		{"account-level 400 organization disabled", http.StatusBadRequest, "This organization has been disabled.", "api_error", http.StatusBadRequest},
		{"account-level 400 credit balance", http.StatusBadRequest, "Your credit balance is too low to access the API.", "api_error", http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			service.SetOpsUpstreamError(c, tc.status, tc.message, "")

			phase, _, errorOwner, _ := classifyOpsErrorLog(c, tc.errType, tc.message, "", tc.final)

			require.Equal(t, "upstream", phase, "genuine provider/account-health errors must stay upstream")
			require.Equal(t, "provider", errorOwner, "must keep counting toward upstream_error_rate")
		})
	}
}

// Routing capacity ("no available accounts") must still win over the
// client-induced branch so the existing SLA-exclusion behaviour is preserved.
func TestClassifyOpsRoutingCapacityWinsOverClientInduced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	markOpsRoutingCapacityLimited(c)
	service.SetOpsUpstreamError(c, http.StatusBadRequest,
		"The 'gpt-4o' model is not supported when using Codex with a ChatGPT account.", "")

	phase, isBusinessLimited, errorOwner, _ := classifyOpsErrorLog(c, "api_error",
		"No available accounts", "", http.StatusServiceUnavailable)

	require.Equal(t, "routing", phase)
	require.True(t, isBusinessLimited)
	require.Equal(t, "platform", errorOwner)
}
