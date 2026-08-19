package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldFailoverOpenAIUpstreamError_HTTPAndSSEShareDecision(t *testing.T) {
	overloadedEchoedContext := []byte(`{"type":"response.failed","response":{"id":"resp_failed","object":"response","model":"gpt-5.6","status":"failed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"isOpenAIContextWindowError reports whether the caller exceeded the context window"}]}],"error":{"code":"invalid_request_error","type":"invalid_request_error","message":"Our servers are currently overloaded. Please try again later."}}}`)
	overloadedMsg := "Our servers are currently overloaded. Please try again later."
	contextWindowBody := []byte(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"upstream_error","code":null}}`)
	contextWindowMsg := "Your input exceeds the context window of this model. Please adjust your input and try again."
	contentPolicy := []byte(`{"type":"response.failed","error":{"code":"content_policy","message":"request blocked by policy"}}`)
	rateLimit := []byte(`{"type":"response.failed","response":{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached"}}}`)
	serverError := []byte(`{"type":"response.failed","error":{"code":"server_error","message":"upstream processing failed"}}`)

	tests := []struct {
		name       string
		status     int
		message    string
		body       []byte
		ssePayload []byte
		want       bool
	}{
		{
			name:       "overloaded 400 with echoed context-window text failovers",
			status:     http.StatusBadRequest,
			message:    overloadedMsg,
			body:       overloadedEchoedContext,
			ssePayload: overloadedEchoedContext,
			want:       true,
		},
		{
			name:       "real context-window 400 is terminal",
			status:     http.StatusBadRequest,
			message:    contextWindowMsg,
			body:       contextWindowBody,
			ssePayload: contextWindowBody,
			want:       false,
		},
		{
			name:       "content_policy is caller-fault",
			status:     http.StatusBadRequest,
			message:    "request blocked by policy",
			body:       contentPolicy,
			ssePayload: contentPolicy,
			want:       false,
		},
		{
			name:       "rate_limit failovers",
			status:     http.StatusTooManyRequests,
			message:    "Rate limit reached",
			body:       rateLimit,
			ssePayload: rateLimit,
			want:       true,
		},
		{
			name:       "unknown server_error failovers",
			status:     http.StatusBadGateway,
			message:    "upstream processing failed",
			body:       serverError,
			ssePayload: serverError,
			want:       true,
		},
		{
			name:    "generic HTTP 400 without transient signal stays terminal",
			status:  http.StatusBadRequest,
			message: "Missing required parameter: 'instructions'",
			body:    []byte(`{"error":{"message":"Missing required parameter: 'instructions'"}}`),
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldFailoverOpenAIUpstreamError(tc.status, tc.message, tc.body))
			require.Equal(t, tc.want, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(tc.status, tc.message, tc.body))
			if len(tc.ssePayload) == 0 {
				return
			}
			sseWant := openAIStreamFailedEventShouldFailover(tc.ssePayload, tc.message)
			require.Equal(t, tc.want, sseWant, "SSE adapter must match the HTTP SSOT for this payload")
			require.Equal(t, shouldFailoverOpenAIUpstreamError(
				openAIStreamFailedEventSemanticStatus(tc.ssePayload, tc.message),
				tc.message,
				tc.ssePayload,
			), sseWant, "SSE wrapper must be semantic-status + SSOT only")
		})
	}
}
