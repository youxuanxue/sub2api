package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
)

var kiroTransportFailoverBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

var (
	errKiroEmptyResponse         = errors.New("kiro upstream returned an empty response")
	errKiroUnsupportedStopReason = errors.New("kiro upstream returned an unsupported stop reason")
)

const (
	kiroContentFilteredClientText = "Request was blocked by upstream content filtering"
)

// KiroContentFilteredError represents Kiro's successful HTTP transport with a
// policy terminal outcome and no assistant output. Retrying another account
// cannot change the request-level policy decision.
type KiroContentFilteredError struct{}

func (*KiroContentFilteredError) Error() string {
	return "kiro content filter rejected the request"
}

func KiroContentFilteredClientMessage() string {
	return kiroContentFilteredClientText
}

// KiroInvalidModelError is a typed, status-carrying error raised when the Kiro
// (sixth platform) upstream rejects a request with HTTP 400 INVALID_MODEL_ID —
// i.e. the requested model is not one the Kiro/CodeWhisperer backend serves.
//
// It mirrors the BetaBlockedError / PromptTooLongError pattern so the gateway
// handler can `errors.As` it and return a clean 400 invalid_request_error to the
// client instead of the previous failure mode, where forwardStreaming had
// already emitted message_start (started=true), the `!enc.started` guard was
// defeated, and the request returned an empty but "successful" 200 SSE stream.
//
// A model-unsupported error is intentionally NOT a cross-account failover
// trigger: every Kiro account would reject the same unknown model identically,
// so failover would only burn accounts before returning the same 400.
type KiroInvalidModelError struct {
	StatusCode int
	Model      string
	Body       string
}

// KiroInvalidRequestError is a local or upstream validation rejection for the
// caller's translated request. Retrying another Kiro account cannot change the
// result.
type KiroInvalidRequestError struct {
	StatusCode int
	Message    string
	Body       string
	// ContextOverflow marks the local payload-size guard specifically: the
	// conversation outgrew the serving model's INPUT limit. It is never set for
	// an upstream rejection. Classification is carried as a field rather than
	// inferred from StatusCode because the public status for this case is an
	// ordinary 400, indistinguishable from any other validation rejection.
	ContextOverflow bool
}

// KiroEndpointQuotaExhaustedError is raised when every Kiro upstream endpoint
// returned HTTP 429 quota exhaustion during the automatic fallback loop.
type KiroEndpointQuotaExhaustedError struct {
	Body string
}

func (e *KiroEndpointQuotaExhaustedError) Error() string {
	if e == nil {
		return "kiro endpoint quota exhausted"
	}
	if strings.TrimSpace(e.Body) != "" {
		return e.Body
	}
	return "kiro endpoint quota exhausted"
}

func (e *KiroEndpointQuotaExhaustedError) ClientMessage() string {
	// Public wire SSOT for upstream-limit 429; ops/detail keep
	// tkKiroEndpointQuotaExhaustedClient via detectors and annotate paths.
	return TkClientFacingBusy429Message
}

func (e *KiroInvalidModelError) Error() string {
	return fmt.Sprintf("kiro invalid model %q: status=%d", e.Model, e.StatusCode)
}

// ClientMessage is the operator/end-user-facing text written into the relay
// error body. Kept terse and model-scoped so the caller knows exactly which
// model the Kiro platform refused.
func (e *KiroInvalidModelError) ClientMessage() string {
	return fmt.Sprintf("model %s is not supported", e.Model)
}

func (e *KiroInvalidRequestError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "kiro rejected the request"
	}
	return fmt.Sprintf("kiro rejected request: %s", e.Message)
}

// ClientMessage returns the public copy. Both oversize cases drop byte counts,
// truncation state and the fulfilling supplier — internal detail the caller
// cannot act on — but they are different failures and get different copy: a
// local context overflow gets the actionable prompt-length text, an upstream
// byte-size 413 the bare request-too-large SSOT. Any other message that still
// names the supplier collapses too, rather than shipping the name.
func (e *KiroInvalidRequestError) ClientMessage() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "The request was rejected"
	}
	// A local context overflow is an input-limit failure, not a byte-size one:
	// it gets the actionable "prompt is too long" copy the Anthropic-shaped
	// clients key on. An upstream byte-size 413 keeps the bare SSOT.
	if e.ContextOverflow {
		return TkClientFacingContextOverflowMessage
	}
	if e.ClientStatusCode() == http.StatusRequestEntityTooLarge {
		return TkClientFacingRequestTooLargeMessage
	}
	msg := sanitizeUpstreamErrorMessage(e.Message)
	if TkLooksLikeUpstreamSupplierLeakMessage(msg) {
		return "The request was rejected"
	}
	return msg
}

// ClientStatusCode returns the public status for a Kiro request-validation
// failure. Upstream validation errors historically omit a status and remain
// ordinary 400s, and the local context-overflow guard is a 400 as well. A 413
// survives only when an upstream enforced its own HTTP body byte ceiling.
func (e *KiroInvalidRequestError) ClientStatusCode() int {
	if e == nil || e.StatusCode == 0 {
		return http.StatusBadRequest
	}
	// The local input-limit guard reports 400: 413 request_too_large is the
	// per-endpoint byte ceiling in Anthropic's contract, and answering a
	// context overflow with it makes Claude Code show the model as
	// unavailable instead of surfacing the prompt length.
	if e.ContextOverflow {
		return http.StatusBadRequest
	}
	return e.StatusCode
}

// ClientErrorType keeps Messages and Chat Completions adapters aligned on the
// caller-owned overflow classification.
func (e *KiroInvalidRequestError) ClientErrorType() string {
	if e.ClientStatusCode() == http.StatusRequestEntityTooLarge {
		return "request_too_large"
	}
	// ContextOverflow lands here as invalid_request_error, matching the native
	// Anthropic shape for a prompt that outgrew the context window.
	return "invalid_request_error"
}

// classifyKiroForwardError applies the Kiro error policy at the boundary between
// the protocol client and the gateway: request/model validation stays a direct
// 400, account/upstream health failures become *UpstreamFailoverError, and
// transport/read failures become a generic 502 failover signal without leaking
// proxy or socket details to clients.
//
// The vendored client formats non-200 responses as
//
//	HTTP <code> from <endpoint>: <raw upstream body>
//
// (see internal/integration/kiro/client.go CallKiroAPIWithDoer). We match on
// the literal "HTTP 400" prefix plus the upstream "INVALID_MODEL_ID" marker so
// an unrelated 400 (or a 400 with a different reason) does not get mislabeled
// as a model error.
func isKiroEndpointQuotaExhaustedError(msg string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(msg)), "quota exhausted on")
}

type kiroForwardErrorObservation struct {
	Kind   string
	Reason string
	Status int
}

func classifyAndRecordKiroForwardError(c *gin.Context, account *Account, err error, model string) error {
	observation, classified := classifyKiroForwardError(err, model)
	var contentFilteredErr *KiroContentFilteredError
	if errors.As(classified, &contentFilteredErr) {
		MarkOpsClientContentFiltered(c)
	}
	if observation != nil {
		recordKiroForwardError(c, account, err, *observation)
	}
	return classified
}

func classifyKiroForwardError(err error, model string) (*kiroForwardErrorObservation, error) {
	if err == nil {
		return nil, nil
	}
	var contentFilteredErr *KiroContentFilteredError
	if errors.As(err, &contentFilteredErr) {
		return nil, contentFilteredErr
	}
	msg := err.Error()
	if isKiroEndpointQuotaExhaustedError(msg) {
		return nil, &KiroEndpointQuotaExhaustedError{Body: msg}
	}
	if isKiroInvalidModelError(msg) {
		return nil, &KiroInvalidModelError{
			StatusCode: 400,
			Model:      model,
			Body:       msg,
		}
	}
	if statusCode, body, ok := parseKiroHTTPError(msg); ok {
		if invalid, matched := classifyKiroImageClientRejection(statusCode, body, msg, false); matched {
			invalid.Body = msg
			return nil, invalid
		}
		if statusCode == http.StatusBadRequest && isKiroValidationErrorBody(body) && !isKiroProfileArnError(msg) {
			message := strings.TrimSpace(extractUpstreamErrorMessage(body))
			if message == "" {
				message = "The request was rejected"
			}
			return nil, &KiroInvalidRequestError{StatusCode: statusCode, Message: message, Body: msg}
		}
		if obs, failover, matched := kiroJSONForbiddenFailover(statusCode, msg, body); matched {
			return obs, failover
		}
		return nil, &UpstreamFailoverError{
			StatusCode:   statusCode,
			ResponseBody: body,
		}
	}
	if statusCode, body, ok := parseKiroEventStreamError(msg); ok {
		if invalid, matched := classifyKiroImageClientRejection(statusCode, body, msg, true); matched {
			invalid.Body = msg
			return nil, invalid
		}
		if statusCode == http.StatusBadRequest && strings.Contains(strings.ToUpper(msg), "INVALID_MODEL_ID") {
			return nil, &KiroInvalidModelError{
				StatusCode: statusCode,
				Model:      model,
				Body:       msg,
			}
		}
		if statusCode == http.StatusBadRequest && !isKiroProfileArnError(msg) {
			message := strings.TrimSpace(extractUpstreamErrorMessage(body))
			if message == "" {
				message = "The request was rejected"
			}
			return nil, &KiroInvalidRequestError{StatusCode: statusCode, Message: message, Body: msg}
		}
		if obs, failover, matched := kiroJSONForbiddenFailover(statusCode, msg, body); matched {
			return obs, failover
		}
		return nil, &UpstreamFailoverError{
			StatusCode:   statusCode,
			ResponseBody: body,
		}
	}
	// A transport failure means this Kiro account/egress could not reach the
	// upstream at all. Return the same failover shape as other gateway
	// transports so the handler can try another account instead of immediately
	// exposing a client-side "connection refused" error.
	if observation, ok := classifyKiroOpaqueFailure(err); ok {
		return &observation, &UpstreamFailoverError{
			StatusCode:   http.StatusBadGateway,
			ResponseBody: append([]byte(nil), kiroTransportFailoverBody...),
		}
	}
	return nil, fmt.Errorf("kiro upstream call failed: %w", err)
}

func classifyKiroOpaqueFailure(err error) (kiroForwardErrorObservation, bool) {
	switch {
	case errors.Is(err, kiroproto.ErrInvalidEventStream):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "invalid_event_stream"}, true
	case errors.Is(err, kiroproto.ErrInvalidToolUse):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "invalid_tool_use"}, true
	case errors.Is(err, errKiroEmptyResponse):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "empty_response"}, true
	case errors.Is(err, errKiroUnsupportedStopReason):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "unsupported_stop_reason"}, true
	case errors.Is(err, io.ErrUnexpectedEOF):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "unexpected_eof"}, true
	case errors.Is(err, io.EOF):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "eof"}, true
	case errors.Is(err, context.DeadlineExceeded):
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "deadline_exceeded"}, true
	case errors.Is(err, syscall.ECONNREFUSED):
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "connection_refused"}, true
	case errors.Is(err, syscall.ECONNRESET):
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "connection_reset"}, true
	case errors.Is(err, syscall.EHOSTUNREACH):
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "host_unreachable"}, true
	case errors.Is(err, syscall.ENETUNREACH):
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "network_unreachable"}, true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "network_timeout"}, true
	}

	lower := strings.ToLower(err.Error())
	for _, match := range []struct {
		marker string
		reason string
	}{
		{marker: "connection refused", reason: "connection_refused"},
		{marker: "connection reset by peer", reason: "connection_reset"},
		{marker: "no route to host", reason: "host_unreachable"},
		{marker: "network is unreachable", reason: "network_unreachable"},
		{marker: "no such host", reason: "dns_error"},
		{marker: "tls handshake timeout", reason: "tls_handshake_timeout"},
		{marker: "i/o timeout", reason: "io_timeout"},
	} {
		if strings.Contains(lower, match.marker) {
			return kiroForwardErrorObservation{Kind: "request_error", Reason: match.reason}, true
		}
	}
	if isKiroTransportError(err) {
		return kiroForwardErrorObservation{Kind: "request_error", Reason: "transport_error"}, true
	}
	return kiroForwardErrorObservation{}, false
}

func recordKiroForwardError(c *gin.Context, account *Account, err error, observation kiroForwardErrorObservation) {
	if c == nil || err == nil {
		return
	}
	safeErr := truncateString(sanitizeUpstreamErrorMessage(err.Error()), 2048)
	setOpsUpstreamError(c, observation.Status, safeErr, "")
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformKiro,
		UpstreamStatusCode: observation.Status,
		Kind:               observation.Kind,
		Reason:             observation.Reason,
		Message:            safeErr,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func isKiroProfileArnError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "invalid profilearn") ||
		strings.Contains(lower, "profilearn is required")
}

// classifyKiroImageClientRejection maps request-shaped image rejections to a
// non-failover client error. Matching uses the primary upstream message only
// (never echo/other JSON fields). Provider-class exceptions / non-400 HTTP that
// only echo the exact unsupported-format string must still failover.
func classifyKiroImageClientRejection(statusCode int, body []byte, rawMsg string, fromEventStream bool) (*KiroInvalidRequestError, bool) {
	primary := strings.TrimSpace(extractUpstreamErrorMessage(body))
	candidate := primary
	if candidate == "" {
		// Bedrock sometimes wraps as plain text after "HTTP 502 ...: Bedrock error message: ..."
		candidate = strings.TrimSpace(string(body))
	}
	if !isKiroImageClientError(candidate) {
		return nil, false
	}
	// Mirror input-too-long: InternalServerException (etc.) wins over echoed
	// request-shaped text so provider outages are not masked as client 400s.
	if isKiroProviderExceptionMessage(rawMsg) {
		return nil, false
	}
	if !fromEventStream && statusCode != http.StatusBadRequest && isExactUnsupportedImageFormatMessage(candidate) {
		return nil, false
	}
	message := primary
	if message == "" {
		message = candidate
	}
	if message == "" {
		message = "Image format is not supported"
	}
	return &KiroInvalidRequestError{
		StatusCode: http.StatusBadRequest,
		Message:    message,
	}, true
}

func isExactUnsupportedImageFormatMessage(msg string) bool {
	return strings.EqualFold(strings.TrimSpace(msg), "Image format is not supported.") ||
		strings.EqualFold(strings.TrimSpace(msg), "Image format is not supported")
}

// isKiroProviderExceptionMessage reports AWS exception frames that are account/
// infrastructure failures rather than request validation.
func isKiroProviderExceptionMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range []string{
		"internalserverexception",
		"serviceunavailableexception",
		"dependencyfailedexception",
		"throttlingexception",
		"servicequotaexceededexception",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isKiroImageClientError reports request-shaped image rejections from Kiro /
// Bedrock. These must not failover: every account rejects the same bytes.
func isKiroImageClientError(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	markers := []string{
		"image format is not supported",
		"could not process image",
		"the image was specified using the",
		"appears to be a image/",
		"appears to be an image/",
		"unsupported image format",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// Generic media-type wording must also mention image (avoid Content-Type false positives).
	if strings.Contains(lower, "image") &&
		(strings.Contains(lower, "unsupported media type") || strings.Contains(lower, "media type is not supported")) {
		return true
	}
	// Bedrock MIME mismatch: "...image/png media type, but the image appears..."
	if strings.Contains(lower, "media type") && strings.Contains(lower, "image") &&
		(strings.Contains(lower, "appears to be") || strings.Contains(lower, "but the image")) {
		return true
	}
	return false
}

func isKiroValidationErrorBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	return kiroproto.IsUnsupportedImageFormatError(body) ||
		strings.Contains(lower, "validationexception") ||
		strings.Contains(lower, "invalidrequestexception") ||
		isKiroInputTooLongError(lower)
}

func isKiroInputTooLongError(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "content_length_exceeds_threshold") ||
		strings.Contains(lower, "input is too long") ||
		strings.Contains(lower, "input exceeds the context window") ||
		strings.Contains(lower, "input exceeds the context length")
}

// parseKiroEventStreamError maps AWS EventStream exception frames to the same
// status-bearing shape used for ordinary HTTP errors. EventStream failures are
// delivered inside an HTTP 200 response, so the low-level parser reports the
// exception name and this classifier supplies the semantic status.
func parseKiroEventStreamError(msg string) (int, []byte, bool) {
	if !strings.Contains(strings.ToLower(msg), "kiro event stream error:") {
		return 0, nil, false
	}
	lower := strings.ToLower(msg)
	status := 0
	switch {
	case strings.Contains(lower, "throttlingexception"),
		strings.Contains(lower, "servicequotaexceededexception"):
		status = http.StatusTooManyRequests
	case strings.Contains(lower, "unauthorizedexception"):
		status = http.StatusUnauthorized
	case strings.Contains(lower, "accessdeniedexception"):
		status = http.StatusForbidden
	case strings.Contains(lower, "validationexception"),
		strings.Contains(lower, "invalidrequestexception"),
		strings.Contains(lower, "invalidmodelexception"),
		strings.Contains(lower, "modelnotfound"):
		status = http.StatusBadRequest
	case strings.Contains(lower, "resourcenotfoundexception"):
		status = http.StatusNotFound
	case strings.Contains(lower, "conflictexception"):
		status = http.StatusConflict
	case strings.Contains(lower, "internalserverexception"),
		strings.Contains(lower, "serviceunavailableexception"),
		strings.Contains(lower, "dependencyfailedexception"):
		status = http.StatusBadGateway
	case isKiroInputTooLongError(lower):
		status = http.StatusBadRequest
	default:
		// Unknown exception names are still upstream failures. 502 preserves
		// failover without pretending the provider returned a client 4xx.
		status = http.StatusBadGateway
	}
	body := []byte(msg)
	if start := strings.Index(msg, "{"); start >= 0 {
		body = []byte(strings.TrimSpace(msg[start:]))
	}
	return status, body, true
}

func isKiroTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection refused",
		"connection reset by peer",
		"no route to host",
		"network is unreachable",
		"no such host",
		"tls handshake timeout",
		"i/o timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// isKiroInvalidModelError reports whether the formatted upstream error string
// represents an HTTP 400 INVALID_MODEL_ID rejection.
func isKiroInvalidModelError(msg string) bool {
	if !strings.Contains(msg, "HTTP 400") {
		return false
	}
	return strings.Contains(strings.ToUpper(msg), "INVALID_MODEL_ID")
}

// kiroJSONForbiddenFailover turns a JSON Kiro 403 into a structured failover
// error. Live bodies are small objects such as {"message":"..."} or
// {"__type":"AccessDeniedException"}, which fail looksLikeStructuredErrorJSON
// and stop the chat-completions loop on that account. HTML and empty 403s stay
// on the fail-fast path.
func kiroJSONForbiddenFailover(statusCode int, rawMsg string, body []byte) (*kiroForwardErrorObservation, error, bool) {
	trimmed := bytes.TrimSpace(body)
	if statusCode != http.StatusForbidden || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, nil, false
	}
	message := "upstream forbidden"
	if strings.Contains(strings.ToLower(rawMsg), "accessdeniedexception") ||
		strings.Contains(strings.ToLower(string(trimmed)), "accessdeniedexception") {
		message = "AccessDeniedException"
	}
	wrapped, err := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    "permission_error",
			"message": message,
		},
	})
	if err != nil {
		return nil, nil, false
	}
	return &kiroForwardErrorObservation{
		Kind:   "upstream_error",
		Reason: "access_denied",
		Status: statusCode,
	}, &UpstreamFailoverError{
		StatusCode:   statusCode,
		ResponseBody: wrapped,
	}, true
}

func parseKiroHTTPError(msg string) (int, []byte, bool) {
	// Profile resolution wraps upstream errors ("resolve profileArn: HTTP ..."),
	// so locate the protocol marker instead of requiring it at byte zero.
	marker := strings.Index(msg, "HTTP ")
	if marker < 0 {
		return 0, nil, false
	}
	rest := msg[marker+len("HTTP "):]
	space := strings.IndexByte(rest, ' ')
	if space <= 0 {
		return 0, nil, false
	}
	statusCode, err := strconv.Atoi(rest[:space])
	if err != nil || statusCode < 100 || statusCode > 599 {
		return 0, nil, false
	}
	body := []byte(msg)
	if colon := strings.Index(rest, ": "); colon >= 0 {
		bodyText := strings.TrimSpace(rest[colon+2:])
		if bodyText != "" {
			body = []byte(bodyText)
		}
	}
	return statusCode, body, true
}
