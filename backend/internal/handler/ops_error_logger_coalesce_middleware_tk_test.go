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

func TestOpsErrorLoggerCoalescedTailSurvivesClientStopping(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 16)
	clock := &fakeClock{now: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)}
	coalescer := newOpsCoalescer(time.Minute, 16, clock.Now)
	t.Cleanup(swapOpsErrorLogCoalescer(coalescer))
	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	key := &service.APIKey{ID: 362, User: &service.User{ID: 16}}
	router := quotaRejectRouter(ops, key)
	for i := 0; i < 8000; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.Equal(t, http.StatusTooManyRequests, w.Code)
	}
	first := drainOpsErrorLogQueue(t)
	require.Len(t, first, 1)
	coalescer.flushClosed(false)
	require.Empty(t, drainOpsErrorLogQueue(t), "open bucket stays coalesced")
	lastAt := clock.Now()
	clock.Advance(time.Minute)
	coalescer.flushClosed(false)
	tail := drainOpsErrorLogQueue(t)
	require.Len(t, tail, 1)
	require.Equal(t, "[repeated x7999 in the preceding 60s] API key 额度已用完", tail[0].ErrorMessage)
	require.Equal(t, lastAt, tail[0].CreatedAt, "flush must not date an old failure as new")
	require.Equal(t, int64(16), *tail[0].UserID)
	require.Equal(t, int64(362), *tail[0].APIKeyID)
	require.Empty(t, tail[0].RequestID)
	require.Empty(t, tail[0].ErrorBody)
	coalescer.flushClosed(true)
	require.Empty(t, drainOpsErrorLogQueue(t), "a tail is emitted exactly once")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	restarted := drainOpsErrorLogQueue(t)
	require.Len(t, restarted, 1)
	require.Equal(t, "API key 额度已用完", restarted[0].ErrorMessage, "new bucket must not recount the flushed tail")
}

func TestOpsErrorLoggerCoalescedTailFlushesOpenBucketOnShutdown(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 16)
	clock := &fakeClock{now: time.Now()}
	c := newOpsCoalescer(time.Minute, 16, clock.Now)
	ops := &service.OpsService{}
	for i := 0; i < 3; i++ {
		entry := quotaRejectEntry(362)
		entry.ErrorBody = "private body"
		entry.RequestID = "single-request-id"
		c.admitForService(ops, entry, "API_KEY_QUOTA_EXHAUSTED")
	}
	c.flushClosed(true)
	rows := drainOpsErrorLogQueue(t)
	require.Len(t, rows, 1)
	require.Equal(t, "[repeated x2 in the preceding 60s] API key 额度已用完", rows[0].ErrorMessage)
	require.Empty(t, rows[0].ErrorBody)
	require.Empty(t, rows[0].RequestID)
}

func TestOpsErrorLoggerCoalescedTailSurvivesCapacityReclamation(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 16)
	clock := &fakeClock{now: time.Now()}
	c := newOpsCoalescer(time.Minute, 1, clock.Now)
	ops := &service.OpsService{}
	for i := 0; i < 8; i++ {
		c.admitForService(ops, quotaRejectEntry(362), "API_KEY_QUOTA_EXHAUSTED")
	}
	clock.Advance(time.Minute)
	for i := 0; i < opsCoalesceShardCount; i++ {
		clock.Advance(100 * time.Millisecond)
		c.reclaimIdle(clock.Now())
	}
	rows := drainOpsErrorLogQueue(t)
	require.Len(t, rows, 1)
	require.Equal(t, "[repeated x7 in the preceding 60s] API key 额度已用完", rows[0].ErrorMessage)
	require.Zero(t, c.health().Dropped)
	require.Zero(t, c.health().Tracked)
}

// Real worker shutdown must persist the last suppressed requests before closing
// enqueue, not just make flushClosed produce the right in-memory message.
type coalescedShutdownRepo struct {
	service.OpsRepository
	rows chan *service.OpsInsertErrorLogInput
}

func (r *coalescedShutdownRepo) InsertErrorLog(_ context.Context, e *service.OpsInsertErrorLogInput) (int64, error) {
	r.rows <- e
	return 1, nil
}
func (r *coalescedShutdownRepo) BatchInsertErrorLogs(ctx context.Context, entries []*service.OpsInsertErrorLogInput) (int64, error) {
	for _, e := range entries {
		_, _ = r.InsertErrorLog(ctx, e)
	}
	return int64(len(entries)), nil
}
func TestOpsErrorLoggerShutdownPersistsCoalescedTail(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)
	t.Cleanup(func() { resetOpsErrorLoggerStateForTest(t) })
	c := newOpsCoalescer(time.Minute, 16, time.Now)
	t.Cleanup(swapOpsErrorLogCoalescer(c))
	repo := &coalescedShutdownRepo{rows: make(chan *service.OpsInsertErrorLogInput, 8)}
	ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ops.SetMonitoringEnabled(true)
	for i := 0; i < 3; i++ {
		e := quotaRejectEntry(362)
		if c.admitForService(ops, e, opsCodeAPIKeyQuotaExhausted) {
			enqueueOpsErrorLog(ops, e)
		}
	}
	require.True(t, StopOpsErrorLogWorkers())
	close(repo.rows)
	var messages []string
	for row := range repo.rows {
		messages = append(messages, row.ErrorMessage)
	}
	require.ElementsMatch(t, []string{"API key 额度已用完", "[repeated x2 in the preceding 60s] API key 额度已用完"}, messages)
	require.True(t, StopOpsErrorLogWorkers(), "shutdown remains idempotent")
}
