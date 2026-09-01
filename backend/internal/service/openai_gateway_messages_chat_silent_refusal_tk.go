package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

// messagesChatAnthropicOutputStage keeps converter-generated protocol preamble
// attempt-local until text, thinking, or tool output proves that the upstream
// produced a real answer. This preserves account failover for empty streams.
type messagesChatAnthropicOutputStage struct {
	c                      *gin.Context
	writeStreamHeaders     func()
	clientDisconnected     bool
	semanticOutputReleased bool
	pendingSSE             []string
}

func newMessagesChatAnthropicOutputStage(s *OpenAIGatewayService, c *gin.Context, upstreamHeaders http.Header) *messagesChatAnthropicOutputStage {
	return &messagesChatAnthropicOutputStage{
		c:                  c,
		writeStreamHeaders: s.newStreamHeaderWriter(c, upstreamHeaders),
		pendingSSE:         make([]string, 0, 4),
	}
}

func (s *messagesChatAnthropicOutputStage) Emit(events []apicompat.AnthropicStreamEvent) {
	s.emit(events, messagesChatAnthropicEventsHaveSemanticOutput(events))
}

func (s *messagesChatAnthropicOutputStage) Finalize(
	state *apicompat.ChatCompletionsToAnthropicStreamState,
	usage OpenAIUsage,
	account *Account,
	requestID string,
) *UpstreamFailoverError {
	finalEvents := apicompat.FinalizeChatCompletionsAnthropicStream(state)
	finalHasSemanticOutput := messagesChatAnthropicEventsHaveSemanticOutput(finalEvents)
	if !s.semanticOutputReleased && !finalHasSemanticOutput &&
		!openAIUsageHasTokens(&usage) && messagesChatEmptyFinishIsSilentRefusal(state.FinishReason) {
		failoverErr := newOpenAISilentRefusalFailoverError(s.c, account, requestID)
		// Every bridge event is still staged. If bytes were written, they can only
		// be header-wait pings, so the handler may safely switch accounts on the
		// same client stream. Leave the marker false before any write so ordinary
		// empty attempts retain the full account-failover budget.
		if s.c.Writer.Written() {
			failoverErr.SafeToFailoverAfterWrite = true
		}
		return failoverErr
	}
	s.emit(finalEvents, true)
	return nil
}

func (s *messagesChatAnthropicOutputStage) ClientDisconnected() bool {
	return s != nil && s.clientDisconnected
}

func (s *messagesChatAnthropicOutputStage) emit(events []apicompat.AnthropicStreamEvent, release bool) {
	if s == nil || s.clientDisconnected {
		return
	}

	serialized := make([]string, 0, len(events))
	for _, event := range events {
		value, err := apicompat.ResponsesAnthropicEventToSSE(event)
		if err == nil {
			serialized = append(serialized, value)
		}
	}

	if !s.semanticOutputReleased {
		s.pendingSSE = append(s.pendingSSE, serialized...)
		if !release {
			return
		}
		s.semanticOutputReleased = true
		serialized = s.pendingSSE
		s.pendingSSE = nil
	}

	if len(serialized) == 0 {
		return
	}
	s.writeStreamHeaders()
	for _, value := range serialized {
		if _, err := fmt.Fprint(s.c.Writer, value); err != nil {
			s.clientDisconnected = true
			break
		}
	}
	if !s.clientDisconnected {
		s.c.Writer.Flush()
	}
}

func messagesChatAnthropicEventsHaveSemanticOutput(events []apicompat.AnthropicStreamEvent) bool {
	for _, event := range events {
		switch event.Type {
		case "content_block_start", "content_block_delta":
			return true
		}
	}
	return false
}

func messagesChatEmptyFinishIsSilentRefusal(finishReason string) bool {
	switch strings.TrimSpace(finishReason) {
	case "", "stop":
		return true
	default:
		return false
	}
}
