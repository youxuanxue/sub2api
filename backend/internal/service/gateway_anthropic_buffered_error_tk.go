package service

// TK: single owner for "buffered Anthropic SSE assembly produced no message".
//
// The four buffered assembly loops (Anthropic platform CC/Responses and CN
// native-Anthropic CC/Responses) all reduce an upstream SSE stream to one
// *apicompat.AnthropicResponse. apicompat.AnthropicStreamEvent models only
// message_start / message_delta / content_block_*, so a terminal Anthropic
// `error` event unmarshals into a bare Type=="error" and is then skipped by
// every branch. finalResp stays nil and the loop falls through to a generic
// 502 "Upstream stream ended without a response".
//
// Two things went wrong with that fallthrough:
//
//  1. The real upstream reason (insufficient balance, rate limit, overloaded)
//     never reached the client, which sees an opaque gateway 502 and retries.
//  2. No upstream context was recorded, so classifyOpsErrorLog saw errType
//     api_error with no upstream status and produced
//     error_phase=internal / error_owner=platform / error_source=gateway —
//     an upstream fault booked against the gateway, polluting SLA and firing
//     platform-side alerts.
//
// This file owns both the parse and the attribution so the call sites stay two
// lines each. It deliberately does NOT add failover: once an account is chosen
// the buffered path has already consumed the upstream response, and retrying
// here would change scheduling behavior. Failover on 200-with-error-event is
// tracked separately.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// tkAnthropicBufferedUpstreamError is a normalized upstream failure recovered
// from a buffered Anthropic SSE stream that never yielded a message.
type tkAnthropicBufferedUpstreamError struct {
	// ErrType is the Anthropic error type (e.g. "rate_limit_error").
	ErrType string
	// Message is the client-facing upstream message, already sanitized.
	Message string
	// Detail is the raw event payload, retained only for ops logging.
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
func tkParseAnthropicBufferedSSEError(payload []byte) (*tkAnthropicBufferedUpstreamError, bool) {
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
		Detail:         truncateString(string(payload), 2048),
		UpstreamStatus: tkAnthropicErrorTypeUpstreamStatus(errType),
	}, true
}

// tkAnthropicErrorTypeUpstreamStatus maps an Anthropic error type to the HTTP
// status it would have carried had upstream failed the request outright. The
// mapping mirrors the status handling in gateway_upstream_response.go so a
// 200-with-error-event and a real error status classify identically.
func tkAnthropicErrorTypeUpstreamStatus(errType string) int {
	switch strings.TrimSpace(strings.ToLower(errType)) {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error", "forbidden_error":
		return http.StatusForbidden
	case "not_found_error", "model_not_found":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "billing_error", "insufficient_quota":
		return http.StatusPaymentRequired
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

	setOpsUpstreamError(c, upstreamErr.UpstreamStatus, upstreamErr.Message, upstreamErr.Detail)
	kind := "stream_error"
	if upstreamErr.EmptyStream {
		kind = "stream_incomplete"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		UpstreamStatusCode: upstreamErr.UpstreamStatus,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Reason:             upstreamErr.ErrType,
		Message:            upstreamErr.Message,
		Detail:             upstreamErr.Detail,
	})

	return tkAnthropicBufferedClientStatus(upstreamErr.UpstreamStatus),
		tkAnthropicBufferedClientErrType(upstreamErr.ErrType),
		upstreamErr.Message
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
	case "authentication_error", "permission_error", "forbidden_error":
		return "upstream_error"
	default:
		return "server_error"
	}
}
