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

	t.Run("local Anthropic tool context guard is request/client owned", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			Kind:    service.OpsUpstreamKindClientToolContextCorrupt,
			Message: "Invalid Anthropic tool continuation: tool_result must be immediately preceded by the assistant tool_use message.",
		}})

		phase, errorOwner, errorSource := classifyOpsErrorLog(c, "invalid_request_error", "Invalid Anthropic tool continuation", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
		require.Equal(t, "client_request", errorSource)
	})

	t.Run("openai chat/completions unsupported-model 400 (structured invalid_request_error body)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode:   http.StatusBadRequest,
			UpstreamResponseBody: `{"error":{"message":"The 'gpt-5.4-nano' model is not supported when using Codex with a ChatGPT account.","type":"invalid_request_error"}}`,
		}})

		errType := normalizeOpsErrorType("api_error", "")
		phase, errorOwner, errorSource := classifyOpsErrorLog(c, errType, "Upstream request failed", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it feeds upstream_error_rate")
		require.Equal(t, "client_request", errorSource)
	})

	t.Run("openai /v1/responses unsupported-model surfaced as wrapped upstream_error (msg-only signal)", func(t *testing.T) {
		// On /v1/responses the surfaced envelope type is upstream_error and the
		// final status is 502; the only client-induced signal is the upstream
		// message. The upstream status on the context is still 400.
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusBadRequest,
			"The 'codex-mini-latest' model is not supported when using Codex with a ChatGPT account.", "")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})

	t.Run("newapi/volcengine upstream 404 model-not-found (InvalidEndpointOrModel.NotFound) owned by client", func(t *testing.T) {
		// 2026-06-10 false P0: account 7 (volcengine) advertised an un-activated model;
		// every request 404'd on ark and — swallowed into a generic 502 — counted toward
		// upstream_error_rate. The bridge dispatch now records the real upstream 404 via
		// TkRecordBridgeUpstreamError (code prefixed into the message, since the ops
		// classifier reads only the single-field message key), so this must be client-owned.
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusNotFound,
			"InvalidEndpointOrModel.NotFound: The model `doubao-lite-32k-240828` does not exist or you do not have access to it.",
			"InvalidEndpointOrModel.NotFound")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase, "newapi 404 model-not-found must be client-owned, out of upstream_error_rate")
		require.Equal(t, "client", errorOwner)
	})

	t.Run("newapi/dashscope upstream 404 model_not_found (Qwen ct=17) owned by client", func(t *testing.T) {
		// Direct probe 2026-06-10: DashScope (Qwen extension-engine channel) returns the
		// OpenAI-standard 404 model_not_found envelope for a retired/unknown model. The
		// bridge records it code-prefixed; it must be client-owned so a future Qwen model
		// drift cannot re-fire the upstream_error_rate P0 (same class as the volcengine case).
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusNotFound,
			"model_not_found: The model `qwen3.7-max-retired` does not exist or you do not have access to it.",
			"model_not_found")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase, "dashscope 404 model_not_found must be client-owned")
		require.Equal(t, "client", errorOwner)
	})

	t.Run("newapi/deepseek upstream 400 invalid_request_error (DeepSeek ct=43) owned by client", func(t *testing.T) {
		// Direct probe 2026-06-10: DeepSeek (extension-engine channel) returns HTTP 400
		// invalid_request_error for an unknown model ("The supported API model names are
		// deepseek-v4-pro or deepseek-v4-flash, but you passed X."). It rides the existing
		// 400 invalid_request_error branch (not the 404 helper), so model drift on the
		// DeepSeek channel is likewise out of upstream_error_rate.
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusBadRequest,
			"invalid_request_error: The supported API model names are deepseek-v4-pro or deepseek-v4-flash, but you passed deepseek-v4-retired.",
			"invalid_request_error")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "request", phase, "deepseek 400 invalid_request_error must be client-owned")
		require.Equal(t, "client", errorOwner)
	})

	t.Run("genuine newapi upstream 503 stays provider-owned (must keep counting)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusServiceUnavailable, "upstream service temporarily unavailable", "")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream request failed", "", http.StatusBadGateway)

		require.Equal(t, "upstream", phase, "a real 5xx must stay provider-owned")
		require.Equal(t, "provider", errorOwner)
	})

	t.Run("upstream 413 request_too_large is always client-induced", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusRequestEntityTooLarge, "request too large", "")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "request too large", "", http.StatusRequestEntityTooLarge)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})

	t.Run("upstream 400 invalid_request_error via message substring", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		service.SetOpsUpstreamError(c, http.StatusBadRequest,
			`{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`, "")

		phase, errorOwner, _ := classifyOpsErrorLog(c, "api_error", "invalid request", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
	})

	// TK (prod P0 2026-06-06, edge us5): bare "opus" on empty-mapping passthrough
	// accounts → upstream 404 not_found_error. The gateway returns a client 400
	// "Unsupported model", but the captured upstream status is still 404; it must
	// be owned by the client or it keeps driving upstream_error_rate.
	t.Run("anthropic upstream 404 model-not-found is client-induced", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode:   http.StatusNotFound,
			UpstreamResponseBody: `{"type":"error","error":{"type":"not_found_error","message":"model: opus"}}`,
		}})

		phase, errorOwner, _ := classifyOpsErrorLog(c, "api_error", "Unsupported model: opus", "", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it feeds upstream_error_rate")
	})

	// TK (us7 P0 2026-06-13, upstream_error_rate=48.57% false page): the REAL
	// production shape, distinct from the case above. A client hardcoded
	// claude-fable-5; Anthropic access-gates Fable 5 and answers 404
	// not_found_error with the human message "Claude Fable 5 is not available.
	// Please use Opus 4.8". The forward path records the upstream body in Detail
	// (NOT UpstreamResponseBody) and the human message in Message. The case above
	// passed because it (wrongly) put the body in UpstreamResponseBody — a field
	// production never populates on this path — so it never exercised the leak.
	// This case pins the Detail fallback: the not_found_error envelope must be
	// recognized from Detail so the 404 stays client-owned and drops out of
	// upstream_error_rate.
	t.Run("anthropic availability-gating 404 with body only in Detail (real us7 prod shape)", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorMessageKey,
			"Claude Fable 5 is not available. Please use Opus 4.8. Learn more: https://www.anthropic.com/news/fable-mythos-access")
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusNotFound,
			Message:            "Claude Fable 5 is not available. Please use Opus 4.8. Learn more: https://www.anthropic.com/news/fable-mythos-access",
			// Body lives in Detail, NOT UpstreamResponseBody — the gateway forward
			// path populates Detail (gateway.log_upstream_error_body, default on).
			Detail: `{"error":{"message":"Claude Fable 5 is not available. Please use Opus 4.8. Learn more: https://www.anthropic.com/news/fable-mythos-access","type":"not_found_error"},"request_id":"req_011CbzedV3jWa6rNy"}`,
		}})

		phase, errorOwner, _ := classifyOpsErrorLog(c, "api_error", "Unsupported model: claude-fable-5", "", http.StatusBadRequest)

		require.Equal(t, "request", phase, "availability-gating 404 must be client-owned, out of upstream_error_rate")
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it re-fires the us7 false P0")
	})

	// TK (prod us3 P0 2026-06-17, upstream_error_rate=97% false page): a client
	// hammered gpt-5.5-pro on /v1/responses (1604× in 5 min) against a
	// ChatGPT-OAuth (Codex) account that cannot serve that model. The ChatGPT
	// backend answered a bare 404 with an EMPTY error body, so the upstream
	// message/body captured on the context is "". The gateway still passed it
	// through to the caller as not_found_error (handleErrorResponse case 404), but
	// the empty body meant the IsAnthropicModelNotFound404 / IsOpenAICompatModelNotFound404
	// predicates saw "" and — under the old `combined=="" -> return false` — the
	// 404 defaulted to phase=upstream / error_owner=provider, flooding
	// upstream_error_rate. The client-facing not_found_error type is the
	// drift-proof signal: a 404 the gateway owned as not-found is caller-fault even
	// with no upstream body to re-confirm.
	t.Run("openai /v1/responses 404 with EMPTY upstream body but client-facing not_found_error is client-induced", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		// Production shape: only the upstream STATUS is captured; no message, no body.
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusNotFound,
		}})

		phase, errorOwner, _ := classifyOpsErrorLog(c, "not_found_error", "Upstream rejected the request", "", http.StatusNotFound)

		require.Equal(t, "request", phase, "empty-body 404 owned as not_found_error must be client-owned, out of upstream_error_rate")
		require.Equal(t, "client", errorOwner, "must NOT be provider — otherwise it re-fires the us3 false P0")
	})

	// Guard the boundary: a 404 the gateway did NOT own as not-found (masked to a
	// generic upstream_error via ShouldHandleErrorCode=false or a passthrough rule)
	// with an empty upstream body stays provider — the empty-body default must only
	// flip on the positive not_found_error signal, preserving the existing
	// "generic 404 stays provider" contract for the masked path.
	t.Run("openai 404 masked as upstream_error with empty body stays provider", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			UpstreamStatusCode: http.StatusNotFound,
		}})

		phase, errorOwner, _ := classifyOpsErrorLog(c, "upstream_error", "Upstream gateway error", "", http.StatusInternalServerError)

		require.Equal(t, "upstream", phase, "a masked 404 (upstream_error) with no not-found signal stays provider-owned")
		require.Equal(t, "provider", errorOwner)
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
		// A generic 404 that is NOT a model-not-found (no "model" signal) stays provider.
		{"generic upstream 404 not model-not-found", http.StatusNotFound, "resource not found", "upstream_error", http.StatusBadGateway},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			service.SetOpsUpstreamError(c, tc.status, tc.message, "")

			phase, errorOwner, _ := classifyOpsErrorLog(c, tc.errType, tc.message, "", tc.final)

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

	phase, errorOwner, _ := classifyOpsErrorLog(c, "api_error",
		"No available accounts", "", http.StatusServiceUnavailable)

	require.Equal(t, "routing", phase)
	require.Equal(t, "platform", errorOwner)
}
