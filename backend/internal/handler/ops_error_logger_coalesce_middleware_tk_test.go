package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// quotaRejectRouter reproduces the prod reject shape through the real ops
// middleware: the auth middleware's quota 429 with the api key available to the
// logger via the ops fallback key (no ingress-reject marker — a quota rejection
// is not an admission failure, which is exactly why these rows reach the table).
func quotaRejectRouter(ops *service.OpsService, apiKey *service.APIKey) *gin.Engine {
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		middleware2.SetOpsFallbackAPIKey(c, apiKey)
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable)
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code":    "API_KEY_QUOTA_EXHAUSTED",
			"message": "API key 额度已用完",
		})
	})
	return router
}

func drainOpsErrorLogQueue(t *testing.T) []*service.OpsInsertErrorLogInput {
	t.Helper()
	var out []*service.OpsInsertErrorLogInput
	for {
		select {
		case job := <-opsErrorLogQueue:
			out = append(out, job.entry)
		default:
			return out
		}
	}
}

// TestOpsErrorLoggerMiddlewareCoalescesQuotaRejectStorm is the end-to-end cost
// assertion: the measured storm must stop producing one row per request once it
// goes through the real middleware.
func TestOpsErrorLoggerMiddlewareCoalescesQuotaRejectStorm(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 512)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(swapOpsErrorLogCoalescer(newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	apiKey := &service.APIKey{ID: 362, Key: "sk-quota-exhausted", UserID: 16, User: &service.User{ID: 16}}
	router := quotaRejectRouter(ops, apiKey)

	const requests = 300
	for i := 0; i < requests; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		clock.Advance(150 * time.Millisecond) // inside one bucket
	}

	queued := drainOpsErrorLogQueue(t)
	require.Len(t, queued, 1, "300 identical quota rejections must queue one row")
	require.Equal(t, int64(362), *queued[0].APIKeyID)
	require.Equal(t, "API key 额度已用完", queued[0].ErrorMessage)
	require.Equal(t, http.StatusTooManyRequests, queued[0].StatusCode)
	require.Equal(t, "client", queued[0].ErrorOwner)
	require.True(t, queued[0].IsBusinessLimited)
}

// TestOpsErrorLoggerMiddlewareStillRecordsDistinctKeys: two customers hitting
// their own ceilings must each get a row — one storm never hides the other.
func TestOpsErrorLoggerMiddlewareStillRecordsDistinctKeys(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 64)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(swapOpsErrorLogCoalescer(newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, id := range []int64{362, 363} {
		apiKey := &service.APIKey{ID: id, Key: "sk-key", UserID: 16, User: &service.User{ID: 16}}
		router := quotaRejectRouter(ops, apiKey)
		for i := 0; i < 20; i++ {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
			require.Equal(t, http.StatusTooManyRequests, w.Code)
		}
	}

	queued := drainOpsErrorLogQueue(t)
	require.Len(t, queued, 2, "each key keeps its own first occurrence")
	require.ElementsMatch(t, []int64{362, 363}, []int64{*queued[0].APIKeyID, *queued[1].APIKeyID})
}

// TestOpsErrorLoggerMiddlewareStillRecordsUpstreamFailuresEveryTime: the gate
// must not touch provider-owned rows, which carry per-occurrence attribution.
func TestOpsErrorLoggerMiddlewareStillRecordsUpstreamFailuresEveryTime(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 64)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(swapOpsErrorLogCoalescer(newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	apiKey := &service.APIKey{ID: 362, Key: "sk-key", UserID: 16, User: &service.User{ID: 16}}

	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/messages", func(c *gin.Context) {
		middleware2.SetOpsFallbackAPIKey(c, apiKey)
		c.Set(service.OpsUpstreamStatusCodeKey, http.StatusBadGateway)
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "upstream request failed"}})
	})

	const requests = 10
	for i := 0; i < requests; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.Equal(t, http.StatusBadGateway, w.Code)
	}

	require.Len(t, drainOpsErrorLogQueue(t), requests, "upstream failures are never coalesced")
}
