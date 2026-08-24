package service

// TK: single owner for "buffered Anthropic SSE assembly hit a terminal error".
//
// The four buffered assembly loops (Anthropic platform CC/Responses and CN
// native-Anthropic CC/Responses) all reduce an upstream SSE stream to one
// *apicompat.AnthropicResponse. apicompat.AnthropicStreamEvent models only
// message_start / message_delta / content_block_*, so a terminal Anthropic
// `error` event unmarshals into a bare Type=="error" and is then skipped by
// every branch. Two outcomes follow, and both used to be misreported:
//
//  1. No message at all — finalResp stays nil and the loop falls through to a
//     generic 502 "Upstream stream ended without a response". The real upstream
//     reason (insufficient balance, rate limit, overloaded) never reached the
//     client, which sees an opaque gateway 502 and retries. No upstream context
//     was recorded either, so classifyOpsErrorLog saw errType api_error with no
//     upstream status and produced error_phase=internal / error_owner=platform /
//     error_source=gateway — an upstream fault booked against the gateway,
//     polluting SLA and firing platform-side alerts.
//
//  2. Partial message then error — message_start (and possibly content) arrived
//     before the error event, so finalResp is non-nil. The loop returned the
//     truncated content as a clean HTTP 200 and recorded nothing: the client
//     could not tell the answer was cut short, and the provider fault was
//     invisible to ops.
//
// This file owns the parse and the attribution so the call sites stay small.
// It deliberately does NOT add failover: once an account is chosen the buffered
// path has already consumed the upstream response, and retrying here would
// change scheduling behavior. Failover on 200-with-error-event is tracked
// separately.

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// tkAnthropicBufferedUpstreamError is a normalized upstream failure recovered
// from a buffered Anthropic SSE stream.
type tkAnthropicBufferedUpstreamError struct {
	// ErrType is the Anthropic error type (e.g. "rate_limit_error").
	ErrType string
	// Message is the client-facing upstream message, already sanitized.
	Message string
	// Detail is the raw event payload, retained only for ops logging and only
	// when the operator enabled upstream error body logging.
	Detail string
	// UpstreamStatus is the HTTP status the error type corresponds to. It is
	// always > 0 so that recording it flips ops classification to
	// phase=upstream / owner=provider.
	UpstreamStatus int
	// EmptyStream marks the "200 with no usable events" case, where upstream
	// sent no error event at all.
	EmptyStream bool
}

// tkParseAnthropicBufferedSSEError reports whether one SSE data payload is a
// terminal Anthropic `error` event, and normalizes it when so.
//
// cfg gates raw payload capture the same way every other appendOpsUpstreamError
// call site in this package does (see gateway_count_tokens.go); it may be nil.
func tkParseAnthropicBufferedSSEError(payload []byte, cfg *config.Config) (*tkAnthropicBufferedUpstreamError, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	errType := strings.TrimSpace(gjson.GetBytes(payload, "error.type").String())
	if errType == "" {
		errType = "api_error"
	}
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
	if message == "" {
		message = "Upstream returned an error before any response content"
	}
	return &tkAnthropicBufferedUpstreamError{
		ErrType:        errType,
		Message:        message,
		Detail:         tkAnthropicBufferedErrorDetail(payload, cfg),
		UpstreamStatus: tkAnthropicErrorTypeUpstreamStatus(errType),
	}, true
}

// tkAnthropicBufferedErrorDetail returns the raw upstream payload for ops
// logging only when the operator opted into upstream error body capture.
func tkAnthropicBufferedErrorDetail(payload []byte, cfg *config.Config) string {
	if cfg == nil || !cfg.Gateway.LogUpstreamErrorBody {
		return ""
	}
	maxBytes := cfg.Gateway.LogUpstreamErrorBodyMaxBytes
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	return truncateString(string(payload), maxBytes)
}

// tkAnthropicErrorTypeUpstreamStatus maps an Anthropic SSE error type to the
// HTTP status it would have carried had upstream failed the request outright,
// so a 200-with-error-event and a real error status classify identically. Only
// types Anthropic actually puts on the wire are listed; anything else is a
// server-side fault and takes the default.
func tkAnthropicErrorTypeUpstreamStatus(errType string) int {
	switch strings.TrimSpace(strings.ToLower(errType)) {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return statusAnthropicOverloaded
	default:
		return http.StatusInternalServerError
	}
}

// statusAnthropicOverloaded is Anthropic's non-standard overload status.
const statusAnthropicOverloaded = 529

// tkAnthropicBufferedFailure records upstream attribution for a buffered
// assembly that produced no message, and returns the client-facing status,
// error type and message the caller should write.
//
// upstreamErr is nil when the stream simply ended with nothing usable; that is
// still an upstream protocol fault (HTTP 200, no assemblable events), so it is
// attributed to the provider rather than left as a gateway-internal error.
func tkAnthropicBufferedFailure(
	c *gin.Context,
	requestID string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) (status int, errType string, message string) {
	if upstreamErr == nil {
		upstreamErr = &tkAnthropicBufferedUpstreamError{
			ErrType:        "upstream_error",
			Message:        "Upstream stream ended without a response",
			UpstreamStatus: http.StatusBadGateway,
			EmptyStream:    true,
		}
	}

	kind := "stream_error"
	if upstreamErr.EmptyStream {
		kind = "stream_incomplete"
	}
	tkRecordAnthropicBufferedUpstreamError(c, requestID, kind, upstreamErr)

	return tkAnthropicBufferedClientStatus(upstreamErr.UpstreamStatus),
		tkAnthropicBufferedClientErrType(upstreamErr.ErrType),
		upstreamErr.Message
}

// tkAnthropicBufferedPartialFailure records upstream attribution when a
// terminal error event arrived *after* the assembly already had a message.
//
// The caller still returns the partial content: the buffered path has consumed
// the upstream response, the tokens were really produced, and discarding them
// would turn a degraded answer into a hard failure. What must not happen is
// booking it as a clean success — the provider fault has to reach ops, which is
// the whole point of this file. The client-facing shape is intentionally left
// alone here; surfacing truncation to clients needs its own contract decision
// (Anthropic has no "upstream errored" stop_reason to map onto).
func tkAnthropicBufferedPartialFailure(
	c *gin.Context,
	requestID string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) {
	if upstreamErr == nil {
		return
	}
	tkRecordAnthropicBufferedUpstreamError(c, requestID, "stream_truncated", upstreamErr)
	logger.L().Warn("buffered anthropic assembly: upstream error after partial content",
		zap.String("request_id", requestID),
		zap.String("upstream_error_type", upstreamErr.ErrType),
		zap.Int("upstream_status", upstreamErr.UpstreamStatus),
	)
}

// tkRecordAnthropicBufferedUpstreamError is the single place that writes ops
// upstream attribution for this family of failures. Both the status and the
// event are required: setOpsUpstreamError is what classifyOpsErrorLog reads to
// pick phase=upstream / owner=provider, and the event carries the per-attempt
// detail operators triage from.
func tkRecordAnthropicBufferedUpstreamError(
	c *gin.Context,
	requestID string,
	kind string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) {
	setOpsUpstreamError(c, upstreamErr.UpstreamStatus, upstreamErr.Message, upstreamErr.Detail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		UpstreamStatusCode: upstreamErr.UpstreamStatus,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Reason:             upstreamErr.ErrType,
		Message:            upstreamErr.Message,
		Detail:             upstreamErr.Detail,
	})
}

// tkAnthropicBufferedClientStatus maps the synthesized upstream status to a
// client-facing status. 5xx and Anthropic's 529 collapse to 502 the same way
// mapUpstreamStatusCode handles real upstream error statuses.
func tkAnthropicBufferedClientStatus(upstreamStatus int) int {
	switch upstreamStatus {
	case http.StatusUnauthorized, http.StatusForbidden:
		// Upstream credential/permission faults are not the client's to fix.
		return http.StatusBadGateway
	case statusAnthropicOverloaded:
		return http.StatusServiceUnavailable
	}
	return mapUpstreamStatusCode(upstreamStatus)
}

// tkAnthropicBufferedClientErrType keeps error types the OpenAI-compatible
// surfaces already emit, and falls back to server_error otherwise.
func tkAnthropicBufferedClientErrType(errType string) string {
	switch strings.TrimSpace(strings.ToLower(errType)) {
	case "invalid_request_error":
		return "invalid_request_error"
	case "rate_limit_error":
		return "rate_limit_error"
	case "overloaded_error":
		return "overloaded_error"
	case "authentication_error", "permission_error":
		return "upstream_error"
	default:
		return "server_error"
	}
}
