package routes

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
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

// Reproduces universal routing rejecting inside the inherited auth chain,
// before Gin reaches the endpoint. A route-local collector misses this case.
func TestTerminalRouteRegistrarRecordsInheritedEarlyAbort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, useGroup := range []bool{false, true} {
		t.Run(fmt.Sprint("group=", useGroup), func(t *testing.T) {
			router := gin.New()
			var routes gin.IRoutes = router
			path := "/messages"
			if useGroup {
				routes = router.Group("/v1")
				path = "/v1/messages"
			}
			groupID := int64(12)
			routes.Use(func(c *gin.Context) {
				middleware.SetOpsFallbackAPIKey(c, &service.APIKey{ID: 1, GroupID: &groupID})
				service.SetOpsRequestModel(c, "claude-fable-5-1")
				service.MarkOpsRoutingCapacityLimited(c)
				c.AbortWithStatus(http.StatusTooManyRequests)
			})
			sink := &routeTerminalSinkStub{}
			registrar := newTerminalRouteRegistrar(routes, sink)
			reached := false
			registrar.Register(http.MethodPost, "/messages", StreamInference, func(c *gin.Context) { reached = true })
			// Sibling excluded routes must not inherit the collector from the group.
			registrar.Register(http.MethodGet, "/models", Excluded("model_catalog"), func(c *gin.Context) {})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
			require.Equal(t, http.StatusTooManyRequests, w.Code)
			require.False(t, reached)
			require.Len(t, sink.events, 1)
			require.Equal(t, service.TerminalOutcomeFinalEmptyPool429, sink.events[0].Kind)
			require.Equal(t, "claude-fable-5-1", sink.events[0].RequestedModel)
			require.Equal(t, groupID, sink.events[0].GroupID)
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, strings.TrimSuffix(path, "messages")+"models", nil))
			require.Len(t, sink.events, 1)
		})
	}
}

func TestTerminalRouteRegistrarRecordsEarlyWebSocketRejectOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		service.SetOpsRequestModel(c, "gpt-5.4")
		service.MarkOpsRoutingCapacityLimited(c)
		c.AbortWithStatus(http.StatusTooManyRequests)
	})
	sink := &routeTerminalSinkStub{}
	registrar := newTerminalRouteRegistrar(router.Group("/v1"), sink)
	registrar.Register(http.MethodGet, "/responses", WebSocketTurn, func(c *gin.Context) { t.Fatal("handler reached") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Len(t, sink.events, 1)
	require.Equal(t, service.TerminalOutcomeFinalEmptyPool429, sink.events[0].Kind)
}

func TestTerminalRouteRegistrarObservesRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		write      bool
		brokenPipe bool
	}{
		{name: "before_headers"},
		{name: "after_stream_headers", write: true},
		{name: "disconnected_client", write: true, brokenPipe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.Recovery())
			sink := &routeTerminalSinkStub{}
			registrar := newTerminalRouteRegistrar(router.Group("/v1"), sink)
			registrar.Register(http.MethodPost, "/messages", StreamInference, func(c *gin.Context) {
				service.SetOpsRequestModel(c, "claude-sonnet-4-6")
				if tc.write {
					c.Writer.WriteHeader(http.StatusOK)
					_, _ = c.Writer.Write([]byte("event: message_start\n\n"))
					c.Writer.Flush()
				}
				if tc.brokenPipe {
					panic(&net.OpError{Op: "write", Net: "tcp", Err: &os.SyscallError{Syscall: "write", Err: syscall.EPIPE}})
				}
				panic("stream handler failed")
			})
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			if tc.write {
				require.Equal(t, http.StatusOK, w.Code)
				require.Equal(t, "event: message_start\n\n", w.Body.String())
			} else {
				require.Equal(t, http.StatusInternalServerError, w.Code)
			}
			if tc.brokenPipe {
				require.Empty(t, sink.events)
			} else {
				require.Len(t, sink.events, 1)
				require.Equal(t, service.TerminalOutcomeOtherError, sink.events[0].Kind)
			}
		})
	}
}

func TestTerminalRouteRegistrarDoesNotAttributeUnauthenticatedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
	sink := &routeTerminalSinkStub{}
	registrar := newTerminalRouteRegistrar(router, sink)
	registrar.Register(http.MethodPost, "/messages", StreamInference, func(c *gin.Context) { t.Fatal("handler reached") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"model":"untrusted-model"}`)))
	require.Empty(t, sink.events)
}

func TestTerminalRouteRegistrarIgnoresRecoveredUpstreamAttemptError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.Recovery())
	sink := &routeTerminalSinkStub{}
	registrar := newTerminalRouteRegistrar(router, sink)
	registrar.Register(http.MethodPost, "/messages", StreamInference, func(c *gin.Context) {
		service.SetOpsRequestModel(c, "claude-sonnet-4-6")
		_ = c.Error(fmt.Errorf("first attempt failed; next attempt succeeded"))
		c.Status(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/messages", nil))
	require.Len(t, sink.events, 1)
	require.Equal(t, service.TerminalOutcomeSuccess, sink.events[0].Kind)
}
