package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
)

var kiroTransportFailoverBody = []byte(`{"error":{"type":"upstream_error","message":"Upstream request failed"}}`)

var errKiroEmptyResponse = errors.New("kiro upstream returned an empty response")

const (
	KiroOutcomeHeader              = "X-TokenKey-Kiro-Outcome"
	KiroSilentRefusalOutcome       = "silent_refusal"
	kiroSilentRefusalReason        = "metering_without_output"
	kiroSilentRefusalClientMessage = "Upstream service temporarily unavailable"
)

// KiroSilentRefusalError marks a request-owned Kiro outcome: the upstream
// accepted and metered the request but emitted no assistant content. Retrying
// another account cannot change that request, so handlers must terminate the
// failover loop while preserving the existing generic 502 client contract.
type KiroSilentRefusalError struct{}

func (e *KiroSilentRefusalError) Error() string {
	return "kiro upstream metered the request without assistant output"
}

func KiroSilentRefusalClientMessage() string {
	return kiroSilentRefusalClientMessage
}

func IsKiroSilentRefusalRelayResponse(account *Account, header http.Header) bool {
	return account != nil && account.IsKiroMirrorStub() &&
		strings.EqualFold(strings.TrimSpace(header.Get(KiroOutcomeHeader)), KiroSilentRefusalOutcome)
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

// KiroInvalidRequestError is an upstream validation rejection carried inside
// an EventStream response. It is a property of the caller's translated
// request, so retrying another Kiro account cannot change the result.
type KiroInvalidRequestError struct {
	StatusCode int
	Message    string
	Body       string
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
	return tkKiroEndpointQuotaExhaustedClient
}

func (e *KiroInvalidModelError) Error() string {
	return fmt.Sprintf("kiro invalid model %q: status=%d", e.Model, e.StatusCode)
}

// ClientMessage is the operator/end-user-facing text written into the relay
// error body. Kept terse and model-scoped so the caller knows exactly which
// model the Kiro platform refused.
func (e *KiroInvalidModelError) ClientMessage() string {
	return fmt.Sprintf("model %s is not supported by Kiro", e.Model)
}

func (e *KiroInvalidRequestError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "kiro rejected the request"
	}
	return fmt.Sprintf("kiro rejected request: %s", e.Message)
}

func (e *KiroInvalidRequestError) ClientMessage() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Kiro rejected the request"
	}
	return sanitizeUpstreamErrorMessage(e.Message)
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
	Kind               string
	Reason             string
	UpstreamStatusCode int
}

func classifyAndRecordKiroForwardError(c *gin.Context, account *Account, err error, model string) error {
	observation, classified := classifyKiroForwardError(err, model)
	if observation != nil {
		recordKiroForwardError(c, account, err, *observation)
	}
	return classified
}

func classifyKiroForwardError(err error, model string) (*kiroForwardErrorObservation, error) {
	if err == nil {
		return nil, nil
	}
	var silentRefusalErr *KiroSilentRefusalError
	if errors.As(err, &silentRefusalErr) {
		return &kiroForwardErrorObservation{Kind: "silent_refusal", Reason: kiroSilentRefusalReason}, silentRefusalErr
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
		if statusCode == http.StatusBadRequest && isKiroValidationErrorBody(body) && !isKiroProfileArnError(msg) {
			message := strings.TrimSpace(extractUpstreamErrorMessage(body))
			if message == "" {
				message = "Kiro rejected the request"
			}
			return nil, &KiroInvalidRequestError{StatusCode: statusCode, Message: message, Body: msg}
		}
		return nil, &UpstreamFailoverError{
			StatusCode:   statusCode,
			ResponseBody: body,
		}
	}
	if statusCode, body, ok := parseKiroEventStreamError(msg); ok {
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
				message = "Kiro rejected the request"
			}
			return nil, &KiroInvalidRequestError{StatusCode: statusCode, Message: message, Body: msg}
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
	case errors.Is(err, errKiroEmptyResponse):
		return kiroForwardErrorObservation{Kind: "response_error", Reason: "empty_response"}, true
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
	setOpsUpstreamError(c, observation.UpstreamStatusCode, safeErr, "")
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformKiro,
		UpstreamStatusCode: observation.UpstreamStatusCode,
		Kind:               observation.Kind,
		Reason:             observation.Reason,
		Message:            safeErr,
	}
	if account != nil {
		if !account.IsKiroMirrorStub() {
			event.Platform = account.Platform
		}
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func kiroSilentRefusalFromRelay(c *gin.Context, account *Account, header http.Header, statusCode int) (error, bool) {
	if !IsKiroSilentRefusalRelayResponse(account, header) {
		return nil, false
	}
	err := &KiroSilentRefusalError{}
	recordKiroForwardError(c, account, err, kiroForwardErrorObservation{
		Kind:               "silent_refusal",
		Reason:             kiroSilentRefusalReason,
		UpstreamStatusCode: statusCode,
	})
	return err, true
}

func isKiroProfileArnError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "invalid profilearn") ||
		strings.Contains(lower, "profilearn is required")
}

func isKiroValidationErrorBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "validationexception") ||
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
