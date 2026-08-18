package handler

import (
	"net/http"
	"strings"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type TerminalOutcomePolicy string

const terminalOutcomeWebSocketSinkKey = "terminal_outcome_websocket_sink"

const (
	TerminalOutcomeSyncInference   TerminalOutcomePolicy = "sync_inference"
	TerminalOutcomeStreamInference TerminalOutcomePolicy = "stream_inference"
	TerminalOutcomeWebSocketTurn   TerminalOutcomePolicy = "websocket_turn"
	TerminalOutcomeAsyncSubmission TerminalOutcomePolicy = "async_submission"
	TerminalOutcomeExcluded        TerminalOutcomePolicy = "excluded"
)

func TerminalOutcomeMiddleware(sink service.TerminalOutcomeSink, policy TerminalOutcomePolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sink != nil && policy == TerminalOutcomeWebSocketTurn {
			c.Set(terminalOutcomeWebSocketSinkKey, sink)
		}
		c.Next()
		if sink == nil || policy == TerminalOutcomeExcluded || policy == TerminalOutcomeWebSocketTurn {
			return
		}
		model := terminalRequestedModel(c)
		if model == "" {
			return
		}
		sink.Record(service.TerminalOutcomeEvent{
			At:             time.Now().UTC(),
			GroupID:        terminalGroupID(c),
			RequestedModel: model,
			Kind:           terminalHTTPOutcomeKind(c, policy),
		})
	}
}

func RecordWebSocketTerminalOutcome(c *gin.Context, model string, turnErr error, resultPresent bool) {
	if c == nil || strings.TrimSpace(model) == "" {
		return
	}
	value, ok := c.Get(terminalOutcomeWebSocketSinkKey)
	if !ok {
		return
	}
	sink, ok := value.(service.TerminalOutcomeSink)
	if !ok || sink == nil {
		return
	}
	kind := service.TerminalOutcomeOtherError
	if turnErr == nil && resultPresent {
		kind = service.TerminalOutcomeSuccess
	} else if isOpsNoAvailableAccountError(turnErr) {
		kind = service.TerminalOutcomeFinalEmptyPool429
	}
	sink.Record(service.TerminalOutcomeEvent{
		At:             time.Now().UTC(),
		GroupID:        terminalGroupID(c),
		RequestedModel: strings.TrimSpace(model),
		Kind:           kind,
	})
}

func terminalHTTPOutcomeKind(c *gin.Context, policy TerminalOutcomePolicy) service.TerminalOutcomeKind {
	status := c.Writer.Status()
	if policy == TerminalOutcomeStreamInference {
		if streamErr, ok := service.GetOpsStreamError(c); ok {
			status = streamErr.IntendedStatus
		}
	}
	if status == http.StatusTooManyRequests && isOpsRoutingCapacityLimited(c) {
		return service.TerminalOutcomeFinalEmptyPool429
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return service.TerminalOutcomeSuccess
	}
	return service.TerminalOutcomeOtherError
}

func terminalRequestedModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(service.OpsModelKey)
	if !ok {
		return ""
	}
	model, _ := value.(string)
	return strings.TrimSpace(model)
}

func terminalGroupID(c *gin.Context) int64 {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.GroupID == nil {
		return 0
	}
	return *apiKey.GroupID
}
