package service

import (
	"fmt"
	"strings"
)

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

func (e *KiroInvalidModelError) Error() string {
	return fmt.Sprintf("kiro invalid model %q: status=%d", e.Model, e.StatusCode)
}

// ClientMessage is the operator/end-user-facing text written into the relay
// error body. Kept terse and model-scoped so the caller knows exactly which
// model the Kiro platform refused.
func (e *KiroInvalidModelError) ClientMessage() string {
	return fmt.Sprintf("model %s is not supported by Kiro", e.Model)
}

// classifyKiroForwardError inspects an error returned by the vendored Kiro API
// call path and, if it recognizes an HTTP 400 INVALID_MODEL_ID rejection,
// wraps it as a typed *KiroInvalidModelError. Any other error is wrapped with
// the historical "kiro upstream call failed" prefix so existing log/behavior is
// preserved.
//
// The vendored client formats non-200 responses as
//
//	HTTP <code> from <endpoint>: <raw upstream body>
//
// (see internal/integration/kiro/client.go CallKiroAPIWithDoer). We match on
// the literal "HTTP 400" prefix plus the upstream "INVALID_MODEL_ID" marker so
// an unrelated 400 (or a 400 with a different reason) does not get mislabeled
// as a model error.
func classifyKiroForwardError(err error, model string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if isKiroInvalidModelError(msg) {
		return &KiroInvalidModelError{
			StatusCode: 400,
			Model:      model,
			Body:       msg,
		}
	}
	return fmt.Errorf("kiro upstream call failed: %w", err)
}

// isKiroInvalidModelError reports whether the formatted upstream error string
// represents an HTTP 400 INVALID_MODEL_ID rejection.
func isKiroInvalidModelError(msg string) bool {
	if !strings.Contains(msg, "HTTP 400") {
		return false
	}
	return strings.Contains(strings.ToUpper(msg), "INVALID_MODEL_ID")
}
