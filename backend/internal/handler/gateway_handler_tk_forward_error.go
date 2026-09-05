package handler

// TokenKey: passive availability failure tap helper.
//
// Rule §5 (CLAUDE.md): keep upstream-shaped handler files thin. Each gateway
// failure tap site (5 today: gateway_handler.go × 2, chat_completions, responses,
// gemini_v1beta) should be a single line; the errors.As extraction lives here.
//
// Why this helper exists:
// docs/approved/pricing-availability-source-of-truth.md#availability-evidence-owner.
//
// Before: handlers passed statusCode=0 to TKRecordForwardFailure. The
// classifier in pricing_availability_service_tk.go requires UpstreamStatusCode
// to be 4xx to recognize model_not_found bodies; with statusCode=0 a real
// upstream 404 ("Requested entity was not found.") fell into the default soft
// accumulator (upstream_5xx) instead of single-sample → unreachable. The
// strong signal promised by §1.3 of the design never fired in production.
//
// After: TkRecordFailureFromErr unwraps the existing *service.UpstreamFailoverError
// (already used elsewhere by the failover routing logic) and pulls the real
// upstream HTTP status + body. No new error type required — the failover
// error covers ~all gateway error returns that observed a real upstream
// response, which is precisely the population the availability classifier
// cares about.

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkRecordFailureFromErr is the single tap helper used by every gateway
// failure path. It unwraps service.UpstreamFailoverError (the canonical
// gateway error type that carries upstream HTTP status + body) and forwards
// the real status to TKRecordForwardFailure so the availability classifier
// can apply the §1.3 matrix correctly.
//
// Behavior:
//   - svc nil OR err nil → no-op.
//   - err is *UpstreamFailoverError (any depth via errors.As) → extract
//     StatusCode + ResponseBody, classify the body via the existing
//     classifier in pricing_availability_service_tk.go.
//   - otherwise → fall back to the previous behavior (statusCode=0 +
//     err.Error()), which still routes through the soft accumulator path.
//     Pre-flight / before-forward errors that never observed an upstream
//     response correctly belong in this branch.
//
// nil-safety on svc and on the receiver inside TKRecordForwardFailure means
// callers do not need to gate on availability being wired.
func TkRecordFailureFromErr(
	svc *service.GatewayService,
	ctx context.Context,
	platform string,
	model string,
	accountID int64,
	err error,
) {
	if svc == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) {
		// Caller-owned cancellation is not evidence about model health. Recording
		// it as the default upstream_5xx failure can make a fresh availability
		// cell unreachable and hide a callable model from strict discovery.
		return
	}
	statusCode := 0
	body := err.Error()
	network := false

	var foErr *service.UpstreamFailoverError
	if errors.As(err, &foErr) && foErr != nil {
		statusCode = foErr.StatusCode
		// UpstreamFailoverError.ResponseBody is the raw upstream bytes; prefer
		// it over err.Error() because the latter is the formatted "upstream
		// error: 404 (failover)" string that contains no body keywords for
		// classifyFailureKind to match against.
		if len(foErr.ResponseBody) > 0 {
			body = string(foErr.ResponseBody)
		}
	}

	svc.TKRecordForwardFailure(ctx, platform, model, accountID, statusCode, body, network)
}

func (h *GatewayHandler) tkHandleFailoverClientStatus(
	c *gin.Context,
	failoverErr *service.UpstreamFailoverError,
	statusCode int,
	responseBody []byte,
	streamStarted bool,
) bool {
	if failoverErr == nil || failoverErr.ClientStatusCode <= 0 {
		return false
	}
	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误。
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")
	if retryAfter := failoverErr.ResponseHeaders.Get("Retry-After"); retryAfter != "" {
		c.Header("Retry-After", retryAfter)
	}
	message := failoverErr.ClientMessage
	if message == "" {
		message = service.GatewayFailoverClientMessage(failoverErr.ClientStatusCode)
	}
	errType := strings.TrimSpace(failoverErr.ClientErrorType)
	if errType == "" {
		errType = "api_error"
	}
	h.handleStreamingAwareError(c, failoverErr.ClientStatusCode, errType, message, streamStarted)
	return true
}

func (h *GatewayHandler) tkEnrichMappedUpstreamErrorMessage(
	c *gin.Context,
	platform string,
	statusCode int,
	responseBody []byte,
	errMsg string,
) string {
	if statusCode == http.StatusForbidden {
		errMsg = service.TkEnrichForbiddenMessage(c, errMsg)
	}
	if platform == service.PlatformAnthropic {
		if msg := service.ExtractUpstreamErrorMessage(responseBody); msg != "" {
			errMsg = msg
		}
		errMsg = service.TkEnrichClaudeIncidentMessage(errMsg, statusCode)
	}
	return errMsg
}

func (h *GatewayHandler) ensureForwardErrorResponseForError(c *gin.Context, err error, streamStarted bool) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	// A canceled inbound request means the caller has already gone away. Writing a
	// generic 502 here only corrupts access/ops semantics; there is no client left
	// to receive it. Keep the established 499 classification used by failover and
	// concurrency cancellation paths.
	if isClientClosedRequest(c, err) {
		markClientClosedForwardRequest(c)
		return false
	}
	if service.IsResponseCommitted(c) {
		return false
	}
	if c.Writer.Written() {
		streamStarted = true
	}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}
