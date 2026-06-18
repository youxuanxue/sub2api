package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompatHandlersUnservableModelEnvelope pins the behavior change that wires the
// anthropic-platform OpenAI-compat entry points — POST /v1/responses and
// POST /v1/chat/completions served by the NATIVE GatewayHandler (an anthropic group
// is not an OpenAI-compat platform, so gateway_tk_openai_compat_handlers.go routes
// it to h.Gateway.Responses / h.Gateway.ChatCompletions, NOT OpenAIGateway) —
// through tkSelectFailureStatusMessage on a first-attempt account-selection failure.
//
// Before the fix both handlers called tkNoAvailableAccounts unconditionally, so an
// unservable model NAME (e.g. a Codex/OpenAI-SDK client sending model="gpt" or the
// bare alias "opus" to an anthropic group) returned 429 + Retry-After — a retry
// signal SDKs auto-retry-storm for a request that can never succeed (prod
// 2026-06-13: 8x empty-pool 429 from hammering unservable names). The service layer
// already raises service.ErrUnsupportedModel for such names (gateway_service_tk_
// unsupported_model.go), but these two handlers ignored it. They now mirror
// OpenAIGateway (openai_gateway_handler.go:361) and the native /v1/messages path
// (gateway_handler.go tkWriteUnsupportedModelIfApplicable).
//
// The err→(status,type,msg) mapping itself is covered by TestTkSelectFailureStatusMessage;
// these tests assert each endpoint's error ENVELOPE carries the mapped values for
// both the unsupported-model (400, no Retry-After) and genuine empty-pool (429 +
// Retry-After) cases — the exact two-line sequence the fix introduced per handler.
func TestCompatHandlersUnservableModelEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}

	newCtx := func(path string) (*gin.Context, *httptest.ResponseRecorder) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		return c, w
	}

	// Mirrors tkWrapSelectionFailure's two terminal shapes: a pure unservable-name
	// failure (ModelUnsupported>0, no capacity noise) wraps ErrUnsupportedModel; a
	// capacity/empty-pool failure wraps ErrNoAvailableAccounts.
	unsupported := fmt.Errorf("%w: gpt (total=2 eligible=0 model_unsupported=2)", service.ErrUnsupportedModel)
	emptyPool := fmt.Errorf("%w supporting model: gpt (total=0)", service.ErrNoAvailableAccounts)

	type responsesBody struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	type ccBody struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	t.Run("responses unsupported model -> 400 invalid_request_error, no Retry-After", func(t *testing.T) {
		c, w := newCtx("/v1/responses")
		status, errType, msg := tkSelectFailureStatusMessage(c, unsupported, "gpt")
		h.responsesErrorResponse(c, status, errType, msg)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, w.Header().Get("Retry-After"), "a request that can never succeed must NOT carry a retry hint")
		var body responsesBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, service.TkUnsupportedModelErrType, body.Error.Code)
		assert.Equal(t, service.TkUnsupportedModelMessage("gpt"), body.Error.Message)
	})

	t.Run("responses empty pool -> 429 + Retry-After (preserved)", func(t *testing.T) {
		c, w := newCtx("/v1/responses")
		status, errType, msg := tkSelectFailureStatusMessage(c, emptyPool, "gpt")
		h.responsesErrorResponse(c, status, errType, msg)

		require.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		var body responsesBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "api_error", body.Error.Code)
		assert.Contains(t, body.Error.Message, "No available accounts")
	})

	t.Run("chat/completions unsupported model -> 400 invalid_request_error, no Retry-After", func(t *testing.T) {
		c, w := newCtx("/v1/chat/completions")
		status, errType, msg := tkSelectFailureStatusMessage(c, unsupported, "gpt")
		h.chatCompletionsErrorResponse(c, status, errType, msg)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, w.Header().Get("Retry-After"))
		var body ccBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, service.TkUnsupportedModelErrType, body.Error.Type)
		assert.Equal(t, service.TkUnsupportedModelMessage("gpt"), body.Error.Message)
	})

	t.Run("chat/completions empty pool -> 429 + Retry-After (preserved)", func(t *testing.T) {
		c, w := newCtx("/v1/chat/completions")
		status, errType, msg := tkSelectFailureStatusMessage(c, emptyPool, "gpt")
		h.chatCompletionsErrorResponse(c, status, errType, msg)

		require.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		var body ccBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "api_error", body.Error.Type)
	})
}

// TestCompatHandlersUnservableModelOpsClassification verifies the SECOND cascade
// row (ops attribution): an unservable-model 400 on these two endpoints must log as
// a CLIENT-owned request error (phase=request, error_owner=client, error_source=
// client_request, is_business_limited=false) — NOT a routing-capacity event (the
// 429's mislabel) and NOT a gateway-internal error. Otherwise a client fault keeps
// polluting the TK capacity/gateway dashboards.
//
// It drives the REAL ops pipeline (parseOpsErrorResponse → normalizeOpsErrorType →
// classifyOpsErrorLog) against the actual body each writer emits, mirroring the
// ops_error_logger.go call site (lines ~860/904/906). The /responses envelope
// carries the error type in the `code` field (OpenAI Responses format), not `type`,
// so this also pins the parser recovery of a known error type from `code`.
func TestCompatHandlersUnservableModelOpsClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &GatewayHandler{}
	unsupported := fmt.Errorf("%w: gpt (total=2 eligible=0 model_unsupported=2)", service.ErrUnsupportedModel)

	classify := func(t *testing.T, path string, write func(c *gin.Context)) (phase, owner, source string, businessLimited bool, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		// Mirror the handler: markOps... must NO-OP for ErrUnsupportedModel.
		markOpsRoutingCapacityLimitedIfNoAvailable(c, unsupported)
		require.False(t, isOpsRoutingCapacityLimited(c), "ErrUnsupportedModel must not set the routing-capacity flag")
		write(c)
		status = w.Code
		parsed := parseOpsErrorResponse(w.Body.Bytes())
		normalizedType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)
		phase, businessLimited, owner, source = classifyOpsErrorLog(c, normalizedType, parsed.Message, parsed.Code, status)
		return
	}

	t.Run("chat/completions 400 -> request/client", func(t *testing.T) {
		phase, owner, source, bl, status := classify(t, "/v1/chat/completions", func(c *gin.Context) {
			s, ty, m := tkSelectFailureStatusMessage(c, unsupported, "gpt")
			h.chatCompletionsErrorResponse(c, s, ty, m)
		})
		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "request", phase)
		assert.Equal(t, "client", owner)
		assert.Equal(t, "client_request", source)
		assert.False(t, bl)
	})

	t.Run("responses 400 -> request/client (type carried in code field)", func(t *testing.T) {
		phase, owner, source, bl, status := classify(t, "/v1/responses", func(c *gin.Context) {
			s, ty, m := tkSelectFailureStatusMessage(c, unsupported, "gpt")
			h.responsesErrorResponse(c, s, ty, m)
		})
		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "request", phase, "responses envelope carries type in `code`; ops parser must recover it")
		assert.Equal(t, "client", owner)
		assert.Equal(t, "client_request", source)
		assert.False(t, bl)
	})
}
