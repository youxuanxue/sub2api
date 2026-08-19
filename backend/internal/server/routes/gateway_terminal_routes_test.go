package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTerminalRouteRegistrarRequiresAValidPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registrar := newTerminalRouteRegistrar(router, nil)

	require.Panics(t, func() {
		registrar.Register(http.MethodGet, "/invalid", terminalRoutePolicy{}, func(c *gin.Context) {})
	})
	require.Panics(t, func() {
		registrar.Register(http.MethodGet, "/excluded", Excluded(""), func(c *gin.Context) {})
	})
}

func TestTerminalRouteRegistrarAttachesTerminalMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	sink := &routeTerminalSinkStub{}
	registrar := newTerminalRouteRegistrar(router, sink)
	registrar.Register(http.MethodPost, "/messages", StreamInference, func(c *gin.Context) {
		c.Set(service.OpsModelKey, "claude-sonnet-4-6")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/messages", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Len(t, sink.events, 1)
	require.Equal(t, service.TerminalOutcomeSuccess, sink.events[0].Kind)
}

type routeTerminalSinkStub struct {
	events []service.TerminalOutcomeEvent
}

func (s *routeTerminalSinkStub) Record(event service.TerminalOutcomeEvent) bool {
	s.events = append(s.events, event)
	return true
}

var _ service.TerminalOutcomeSink = (*routeTerminalSinkStub)(nil)
