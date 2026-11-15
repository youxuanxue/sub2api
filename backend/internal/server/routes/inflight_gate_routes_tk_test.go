//go:build unit

package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// resetInFlightRelayForTest restores the process-wide relay gate around a test.
// The cap lives in package-level atomics because it is a process-wide limit, so
// every test must leave it as it found it.
func resetInFlightRelayForTest(t *testing.T) {
	t.Helper()
	previous := middleware.MaxInFlightRelay()
	middleware.SetMaxInFlightRelay(0)
	t.Cleanup(func() { middleware.SetMaxInFlightRelay(previous) })
}

// TestInFlightGateCoversRelayRoutesAndExemptsProbesAndPanel proves the gate's
// real boundary against the production registration path:
//   - a route registered through the terminal registrar (the relay surface) is
//     shed once the process cap is saturated;
//   - /health, /health/inflight, the public settings bootstrap, the panel plane
//     and static assets stay answerable while the gate is saturated.
//
// The routes are mounted exactly as production mounts them: common routes
// directly on the engine, relay routes through newTerminalRouteRegistrar, and a
// representative panel route on an /api/v1 group that never touches the gate.
func TestInFlightGateCoversRelayRoutesAndExemptsProbesAndPanel(t *testing.T) {
	resetInFlightRelayForTest(t)
	middleware.SetMaxInFlightRelay(1)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterCommonRoutes(router)

	// A parked relay route: the one slot stays held until release is closed.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	relay := newTerminalRouteRegistrar(router, nil)
	relay.Register(http.MethodPost, "/v1/gated-test", StreamInference, func(c *gin.Context) {
		entered <- struct{}{}
		<-release
		c.String(http.StatusOK, "ok")
	})

	// A panel route on the admin plane. It is not relay traffic and must never
	// be shed, even while the relay gate is saturated.
	panel := router.Group("/api/v1")
	panel.GET("/settings/public", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0}) })
	panel.GET("/admin/users", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0}) })
	router.GET("/", func(c *gin.Context) { c.String(http.StatusOK, "<html></html>") })

	// Saturate the gate with the one allowed relay request.
	go func() {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/gated-test", nil))
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("parked relay request never entered; gate wiring is broken")
	}
	require.Equal(t, int64(1), middleware.InFlightRelayCount())

	// A second relay request is shed, fast, with retry guidance.
	rejectedBefore := middleware.InFlightRelayRejectedCount()
	shed := httptest.NewRecorder()
	router.ServeHTTP(shed, httptest.NewRequest(http.MethodPost, "/v1/gated-test", nil))
	require.Equal(t, http.StatusServiceUnavailable, shed.Code, shed.Body.String())
	require.Equal(t, "1", shed.Header().Get("Retry-After"))
	require.Equal(t, rejectedBefore+1, middleware.InFlightRelayRejectedCount())
	// A shed request must not consume a slot.
	require.Equal(t, int64(1), middleware.InFlightRelayCount())

	// Everything outside the relay surface stays answerable at the cap.
	exempt := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/health/live", http.StatusOK},
		{http.MethodGet, "/api/v1/settings/public", http.StatusOK},
		{http.MethodGet, "/api/v1/admin/users", http.StatusOK},
		{http.MethodGet, "/", http.StatusOK},
	}
	for _, tc := range exempt {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, tc.want, w.Code, "%s %s must stay exempt from the relay gate", tc.method, tc.path)
	}

	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for middleware.InFlightRelayCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int64(0), middleware.InFlightRelayCount())
}

// TestInFlightGateDisabledLeavesRelayRoutesUngated covers the rollback switch:
// with the cap at 0 the same relay route must serve every request and never
// report a rejection.
func TestInFlightGateDisabledLeavesRelayRoutesUngated(t *testing.T) {
	resetInFlightRelayForTest(t)
	middleware.SetMaxInFlightRelay(0)
	rejectedBefore := middleware.InFlightRelayRejectedCount()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	relay := newTerminalRouteRegistrar(router, nil)
	relay.Register(http.MethodPost, "/v1/gated-test", StreamInference, func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/gated-test", nil))
		require.Equal(t, http.StatusOK, w.Code, "disabled gate must not shed relay traffic")
	}
	require.Equal(t, rejectedBefore, middleware.InFlightRelayRejectedCount())
	require.Equal(t, int64(0), middleware.InFlightRelayCount())
}

// TestInFlightGateRunsAfterAuthentication proves the admission order contract.
//
// The gate must sit immediately before the business handler, i.e. AFTER every
// auth middleware in the chain. Two registration shapes exist in this tree:
// groups that carry apiKeyAuth on the group, and routes that pass it per route.
// If the gate ran before auth, unauthenticated callers could hold every slot
// with invalid credentials and turn the capacity limiter into the outage.
//
// The test saturates the cap and then proves a request rejected by auth never
// consumes a slot, in both shapes.
func TestInFlightGateRunsAfterAuthentication(t *testing.T) {
	resetInFlightRelayForTest(t)
	middleware.SetMaxInFlightRelay(1)

	gin.SetMode(gin.TestMode)
	router := gin.New()

	var authRan, handlerRan int
	// Shape A: auth on the group.
	groupA := router.Group("/v1")
	groupA.Use(func(c *gin.Context) {
		authRan++
		if c.GetHeader("X-Api-Key") != "good" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	})
	relayA := newTerminalRouteRegistrar(groupA, nil)
	relayA.Register(http.MethodPost, "/shape-a", StreamInference, func(c *gin.Context) {
		handlerRan++
		c.String(http.StatusOK, "ok")
	})

	// Shape B: auth passed per route in handlers, as rootRoutes and
	// codexRoutes do.
	relayB := newTerminalRouteRegistrar(router, nil)
	relayB.Register(http.MethodPost, "/shape-b", StreamInference,
		func(c *gin.Context) {
			authRan++
			if c.GetHeader("X-Api-Key") != "good" {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			c.Next()
		},
		func(c *gin.Context) {
			handlerRan++
			c.String(http.StatusOK, "ok")
		},
	)

	// Hold the single slot with one authenticated request.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	holder := newTerminalRouteRegistrar(router, nil)
	holder.Register(http.MethodPost, "/hold", StreamInference, func(c *gin.Context) {
		entered <- struct{}{}
		<-release
		c.String(http.StatusOK, "ok")
	})
	go router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/hold", nil))
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("holder never entered")
	}
	require.Equal(t, int64(1), middleware.InFlightRelayCount())

	for _, path := range []string{"/v1/shape-a", "/shape-b"} {
		authBefore := authRan
		handlerBefore := handlerRan
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil) // no valid key
		router.ServeHTTP(w, req)

		// Auth must have run and refused; the gate must not have answered.
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"%s: auth must reject before admission decides anything", path)
		require.Equal(t, authBefore+1, authRan, "%s: auth must run", path)
		require.Equal(t, handlerBefore, handlerRan, "%s: handler must not run", path)
		require.Empty(t, w.Header().Get("Retry-After"),
			"%s: an unauthorized request must be refused by auth, not shed by the gate", path)
	}

	// The unauthenticated attempts must not have consumed the held slot.
	require.Equal(t, int64(1), middleware.InFlightRelayCount())

	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for middleware.InFlightRelayCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int64(0), middleware.InFlightRelayCount())
}
