//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The wire-size rejection must tell the caller only that the request is too
// large. Byte counts, the truncation attempt and the fulfilling supplier are
// all supply-side detail a caller cannot act on.
func TestKiroInvalidRequestError_RequestTooLargeClientCopyIsBare(t *testing.T) {
	internal := "Request is too large for Kiro after conversation history truncation (991831 bytes; limit 921600 bytes)"
	err := &KiroInvalidRequestError{
		StatusCode: http.StatusRequestEntityTooLarge,
		Message:    internal,
		Body:       internal,
	}

	public := err.ClientMessage()
	require.Equal(t, TkClientFacingRequestTooLargeMessage, public)
	require.Equal(t, "request_too_large", err.ClientErrorType())
	require.Equal(t, http.StatusRequestEntityTooLarge, err.ClientStatusCode())

	// Negative: none of the internal detail may survive into the public copy.
	for _, leak := range []string{"Kiro", "kiro", "991831", "921600", "truncation", "conversation history"} {
		require.NotContains(t, public, leak, "public 413 copy must not carry %q", leak)
	}

	// Internal text is preserved on the error itself for ops/logs.
	require.Contains(t, err.Message, "991831")
	require.Contains(t, err.Error(), "991831")
}

// A non-413 rejection keeps its actionable upstream text, but never the
// supplier name.
func TestKiroInvalidRequestError_NonOversizeCopyDropsSupplierOnly(t *testing.T) {
	actionable := &KiroInvalidRequestError{
		StatusCode: http.StatusBadRequest,
		Message:    "messages: final message must have non-empty content",
	}
	require.Equal(t, "messages: final message must have non-empty content", actionable.ClientMessage())

	named := &KiroInvalidRequestError{
		StatusCode: http.StatusBadRequest,
		Message:    "Kiro rejected the conversation shape",
	}
	require.Equal(t, "The request was rejected", named.ClientMessage())
	require.NotContains(t, strings.ToLower(named.ClientMessage()), "kiro")

	empty := &KiroInvalidRequestError{StatusCode: http.StatusBadRequest}
	require.Equal(t, "The request was rejected", empty.ClientMessage())
	require.NotContains(t, strings.ToLower(empty.ClientMessage()), "kiro")
}

func TestKiroInvalidModelError_ClientCopyOmitsSupplier(t *testing.T) {
	err := &KiroInvalidModelError{Model: "claude-sonnet-5", StatusCode: http.StatusBadRequest}
	public := err.ClientMessage()
	require.Equal(t, "model claude-sonnet-5 is not supported", public)
	require.NotContains(t, strings.ToLower(public), "kiro")
	// Internal Error() may still name it for diagnosis.
	require.Contains(t, strings.ToLower(err.Error()), "kiro")
}

func TestTkLooksLikeUpstreamSupplierLeakMessage(t *testing.T) {
	for _, leak := range []string{
		"Request is too large for Kiro after truncation",
		"kiro rejected the request",
		"CodeWhisperer runtime refused",
	} {
		require.True(t, TkLooksLikeUpstreamSupplierLeakMessage(leak), leak)
	}
	for _, clean := range []string{
		"",
		"Request too large",
		"messages: final message must have non-empty content",
		"Request was blocked by upstream content filtering",
	} {
		require.False(t, TkLooksLikeUpstreamSupplierLeakMessage(clean), clean)
	}
}

// Machine gate: every client-facing message constant that reaches the wire must
// be free of supplier branding. A hand-kept needle list is exactly how
// "No healthy Grok OAuth account..." stayed on the wire after the Kiro fix, so
// the needles are derived from the platform registry and this test enumerates
// the wire-bound constants. Add new client-facing copy here when you add it.
func TestClientFacingConstants_CarryNoSupplierName(t *testing.T) {
	wireBound := map[string]string{
		"TkClientFacingRequestTooLargeMessage":         TkClientFacingRequestTooLargeMessage,
		"TkClientFacingContextOverflowMessage":         TkClientFacingContextOverflowMessage,
		"TkClientFacingUnsupportedMessage":             TkClientFacingUnsupportedMessage,
		"TkClientFacingBusy429Message":                 TkClientFacingBusy429Message,
		"GrokCredentialUnavailableClientMessage":       GrokCredentialUnavailableClientMessage,
		"OpenAIRequestBodyTooLargeClientMessage":       OpenAIRequestBodyTooLargeClientMessage,
		"KiroContentFilteredClientMessage()":           KiroContentFilteredClientMessage(),
		"OpenAISilentRefusalClientMessage()":           OpenAISilentRefusalClientMessage(),
		"gatewayFailoverClientMessage":                 gatewayFailoverClientMessage,
		"geminiCustomCodeSkippedClientMessage":         geminiCustomCodeSkippedClientMessage,
		"openAIUpstreamAccessUnavailableClientMessage": openAIUpstreamAccessUnavailableClientMessage,
	}
	for name, msg := range wireBound {
		require.False(t, TkLooksLikeUpstreamSupplierLeakMessage(msg),
			"%s ships a supplier name on the wire: %q", name, msg)
	}
}

// The needle list must come from the platform registry, not a hand-kept copy.
func TestSupplierLeakNeedles_DerivedFromPlatformRegistry(t *testing.T) {
	for _, platform := range []string{PlatformKiro, PlatformGrok, PlatformAntigravity, PlatformKimi, PlatformDeepseek} {
		require.True(t, TkLooksLikeUpstreamSupplierLeakMessage("served by "+platform),
			"registry platform %q must be gated", platform)
	}
	// Protocol families the caller addresses us with are not supply branding.
	for _, exempt := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
		require.False(t, TkLooksLikeUpstreamSupplierLeakMessage("the "+exempt+" API shape"),
			"protocol family %q must stay usable in client copy", exempt)
	}
}

// The local input-limit rejection must be distinguishable from the byte-size
// ceiling on all three client-facing axes, and must stay free of supply-side
// detail even though its copy is longer than the bare SSOT.
func TestKiroInvalidRequestError_ContextOverflowIsActionable400(t *testing.T) {
	internal := "Request is too large for Kiro after conversation history truncation (1126130 bytes; limit 921600 bytes)"
	err := &KiroInvalidRequestError{
		StatusCode:      http.StatusRequestEntityTooLarge,
		Message:         internal,
		Body:            internal,
		ContextOverflow: true,
	}

	require.Equal(t, http.StatusBadRequest, err.ClientStatusCode(),
		"413 is the byte ceiling; an input overflow is a 400 like native Anthropic")
	require.Equal(t, "invalid_request_error", err.ClientErrorType())

	public := err.ClientMessage()
	require.Equal(t, TkClientFacingContextOverflowMessage, public)
	// The phrase Anthropic-shaped clients key on, plus the one action a caller
	// can take. Without these the client renders "model unavailable".
	require.Contains(t, public, "prompt is too long")
	require.Contains(t, public, "/compact")
	for _, leak := range []string{"Kiro", "kiro", "1126130", "921600", "truncation", "conversation history"} {
		require.NotContains(t, public, leak, "public overflow copy must not carry %q", leak)
	}

	// Internal text is untouched for ops.
	require.Contains(t, err.Message, "1126130")
	require.Contains(t, err.Error(), "1126130")

	// Negative: an upstream byte-ceiling 413 is unaffected by this change.
	upstream := &KiroInvalidRequestError{
		StatusCode: http.StatusRequestEntityTooLarge,
		Message:    "request body exceeds the maximum allowed size",
	}
	require.Equal(t, http.StatusRequestEntityTooLarge, upstream.ClientStatusCode())
	require.Equal(t, "request_too_large", upstream.ClientErrorType())
	require.Equal(t, TkClientFacingRequestTooLargeMessage, upstream.ClientMessage())
	require.NotContains(t, upstream.ClientMessage(), "/compact")
}
