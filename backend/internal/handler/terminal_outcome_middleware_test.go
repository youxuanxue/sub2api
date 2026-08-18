package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type terminalOutcomeSinkStub struct {
	events []service.TerminalOutcomeEvent
}

func (s *terminalOutcomeSinkStub) Record(event service.TerminalOutcomeEvent) bool {
	s.events = append(s.events, event)
	return true
}

func runTerminalOutcomeMiddleware(t *testing.T, sink service.TerminalOutcomeSink, policy TerminalOutcomePolicy, endpoint gin.HandlerFunc) []service.TerminalOutcomeEvent {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/test", func(c *gin.Context) {
		groupID := int64(7)
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID})
		c.Next()
	}, TerminalOutcomeMiddleware(sink, policy), endpoint)
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if stub, ok := sink.(*terminalOutcomeSinkStub); ok {
		return stub.events
	}
	return nil
}

func TestTerminalOutcomeMiddlewareRecordsSyncSuccess(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	events := runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeSyncInference, func(c *gin.Context) {
		setOpsRequestContext(c, " gpt-5.4 ", false)
		c.Status(http.StatusOK)
	})
	require.Equal(t, []service.TerminalOutcomeEvent{{GroupID: 7, RequestedModel: "gpt-5.4", Kind: service.TerminalOutcomeSuccess}}, withoutTerminalTimes(events))
}

func TestTerminalOutcomeMiddlewareRecordsOnlyTypedFinalEmptyPool429(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	events := runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeSyncInference, func(c *gin.Context) {
		setOpsRequestContext(c, "claude-sonnet-4-6", false)
		markOpsRoutingCapacityLimited(c)
		c.Status(http.StatusTooManyRequests)
	})
	require.Equal(t, service.TerminalOutcomeFinalEmptyPool429, events[0].Kind)

	sink = &terminalOutcomeSinkStub{}
	events = runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeSyncInference, func(c *gin.Context) {
		setOpsRequestContext(c, "claude-sonnet-4-6", false)
		c.Status(http.StatusTooManyRequests)
	})
	require.Equal(t, service.TerminalOutcomeOtherError, events[0].Kind)
}

func TestTerminalOutcomeMiddlewareUsesSSETerminalStatus(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	events := runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeStreamInference, func(c *gin.Context) {
		setOpsRequestContext(c, "gpt-5.4", true)
		markOpsRoutingCapacityLimited(c)
		service.MarkOpsStreamError(c, "upstream_error", "capacity exhausted", http.StatusTooManyRequests)
		c.Status(http.StatusOK)
	})
	require.Equal(t, service.TerminalOutcomeFinalEmptyPool429, events[0].Kind)
}

func TestTerminalOutcomeMiddlewareSkipsExcludedMissingModelAndNilSink(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	require.Empty(t, runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeExcluded, func(c *gin.Context) {
		setOpsRequestContext(c, "gpt-5.4", false)
		c.Status(http.StatusOK)
	}))

	sink = &terminalOutcomeSinkStub{}
	require.Empty(t, runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeSyncInference, func(c *gin.Context) {
		c.Status(http.StatusOK)
	}))

	require.NotPanics(t, func() {
		runTerminalOutcomeMiddleware(t, nil, TerminalOutcomeSyncInference, func(c *gin.Context) {
			setOpsRequestContext(c, "gpt-5.4", false)
			c.Status(http.StatusOK)
		})
	})
}

func TestTerminalOutcomeMiddlewareSkipsDynamicallyExcludedAndCanceledRequests(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	require.Empty(t, runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeStreamInference, func(c *gin.Context) {
		setOpsRequestContext(c, "gemini-3.1-pro", false)
		ExcludeTerminalOutcome(c)
		c.Status(http.StatusOK)
	}))

	sink = &terminalOutcomeSinkStub{}
	require.Empty(t, runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeStreamInference, func(c *gin.Context) {
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		setOpsRequestContext(c, "gpt-5.4", true)
		cancel()
		c.Status(http.StatusOK)
	}))
}

func TestRecordWebSocketTerminalOutcomeRecordsPerTurn(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	events := runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeWebSocketTurn, func(c *gin.Context) {
		RecordWebSocketTerminalOutcome(c, "gpt-5.4", nil, true)
		RecordWebSocketTerminalOutcome(c, "gpt-5.4", service.ErrNoAvailableAccounts, false)
		c.Status(http.StatusSwitchingProtocols)
	})
	require.Len(t, events, 2)
	require.Equal(t, service.TerminalOutcomeSuccess, events[0].Kind)
	require.Equal(t, service.TerminalOutcomeFinalEmptyPool429, events[1].Kind)
}

func TestRecordWebSocketTerminalOutcomeSkipsCanceledTurn(t *testing.T) {
	sink := &terminalOutcomeSinkStub{}
	events := runTerminalOutcomeMiddleware(t, sink, TerminalOutcomeWebSocketTurn, func(c *gin.Context) {
		RecordWebSocketTerminalOutcome(c, "gpt-5.4", context.Canceled, false)
		c.Status(http.StatusSwitchingProtocols)
	})
	require.Empty(t, events)
}

func withoutTerminalTimes(events []service.TerminalOutcomeEvent) []service.TerminalOutcomeEvent {
	cloned := append([]service.TerminalOutcomeEvent(nil), events...)
	for i := range cloned {
		cloned[i].At = time.Time{}
	}
	return cloned
}
