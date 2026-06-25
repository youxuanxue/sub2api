package service

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

type openAICompatBufferedRouteKind int

const (
	openAICompatBufferedRouteChat openAICompatBufferedRouteKind = iota
	openAICompatBufferedRouteMessages
)

// openAICompatBufferedMissingTerminalResult mirrors the streaming missing-terminal
// policy: no accumulated content → safe failover; partial content → client error
// without failover (client-visible output must not be replayed on another account).
func (s *OpenAIGatewayService) openAICompatBufferedMissingTerminalResult(
	c *gin.Context,
	account *Account,
	requestID string,
	acc *apicompat.BufferedResponseAccumulator,
	route openAICompatBufferedRouteKind,
) (*OpenAIForwardResult, error) {
	const clientMsg = "Upstream stream ended without a terminal response event"
	if acc != nil && acc.HasContent() {
		if route == openAICompatBufferedRouteMessages {
			s.recordOpenAIMessagesStreamUpstreamError(c, account, requestID, "buffered_missing_terminal", clientMsg)
			writeAnthropicError(c, http.StatusBadGateway, "api_error", clientMsg)
		} else {
			writeChatCompletionsError(c, http.StatusBadGateway, "api_error", clientMsg)
		}
		return nil, fmt.Errorf("upstream stream ended without terminal event")
	}

	failoverMsg := "OpenAI stream ended before a terminal event"
	if route == openAICompatBufferedRouteMessages {
		failoverMsg = "OpenAI messages stream ended before a terminal event"
	}
	return nil, s.newOpenAIStreamFailoverError(c, account, false, requestID, nil, failoverMsg)
}

// openAICompatBufferedFailedResponseResult mirrors the streaming response.failed
// policy. The streaming path only fails over when openAIStreamFailedEventShouldFailover
// agrees (transient / capacity errors); a non-retryable failure (content_policy,
// safety, invalid_request, …) is forwarded to the client instead of replayed on a
// sibling account. The buffered path applies the same gate: retryable → failover,
// non-retryable → surface the upstream message as a client error with no failover.
// cyber_policy is handled by the caller before reaching here.
func (s *OpenAIGatewayService) openAICompatBufferedFailedResponseResult(
	c *gin.Context,
	account *Account,
	requestID string,
	finalResponse *apicompat.ResponsesResponse,
	route openAICompatBufferedRouteKind,
) (*OpenAIForwardResult, error) {
	if finalResponse == nil {
		return nil, fmt.Errorf("openai buffered response.failed: nil response")
	}
	payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse})
	message := openAICompatFailedResponseMessage(finalResponse)
	if openAIStreamFailedEventShouldFailover(payload, message) {
		return nil, s.newOpenAIStreamFailoverError(c, account, false, requestID, payload, message)
	}

	// Non-retryable upstream failure: do not failover (replaying a policy/invalid
	// rejection on another account is pointless and pollutes sibling cooldown
	// attribution). Surface the upstream message to the client, as the streaming
	// path forwards the response.failed event verbatim.
	clientMsg := message
	if clientMsg == "" {
		clientMsg = "Upstream returned a failed response"
	}
	if route == openAICompatBufferedRouteMessages {
		s.recordOpenAIMessagesStreamUpstreamError(c, account, requestID, "buffered_response_failed", clientMsg)
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
	} else {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
	}
	return nil, fmt.Errorf("openai buffered response.failed (non-retryable): %s", message)
}
