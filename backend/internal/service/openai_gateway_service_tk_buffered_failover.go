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

func (s *OpenAIGatewayService) openAICompatBufferedFailedResponseFailover(
	c *gin.Context,
	account *Account,
	requestID string,
	finalResponse *apicompat.ResponsesResponse,
) (*OpenAIForwardResult, error) {
	if finalResponse == nil {
		return nil, fmt.Errorf("openai buffered response.failed: nil response")
	}
	payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse})
	return nil, s.newOpenAIStreamFailoverError(
		c,
		account,
		false,
		requestID,
		payload,
		openAICompatFailedResponseMessage(finalResponse),
	)
}
