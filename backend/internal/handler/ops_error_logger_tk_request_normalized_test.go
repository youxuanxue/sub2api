package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestOpsUpstreamEventsForRecoveredLogging_DropsNormalizeAudit(t *testing.T) {
	in := []*service.OpsUpstreamErrorEvent{
		{Kind: service.OpsUpstreamKindRequestNormalized, Message: "cc_environment_stripped"},
		{UpstreamStatusCode: http.StatusUnauthorized, Message: "oauth expired"},
	}
	out := opsUpstreamEventsForRecoveredLogging(in)
	require.Len(t, out, 1)
	require.Equal(t, http.StatusUnauthorized, out[0].UpstreamStatusCode)
}

func TestOpsErrorLoggerMiddleware_SkipsRecoveredLogForNormalizeAuditOnly(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)
	opsErrorLogOnce.Do(func() {})
	opsErrorLogMu.Lock()
	opsErrorLogQueue = make(chan opsErrorLogJob, 4)
	opsErrorLogMu.Unlock()

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
			Kind:    service.OpsUpstreamKindRequestNormalized,
			Message: "cc_geo_stego_normalized,cc_environment_stripped",
		}})
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(0), OpsErrorLogEnqueuedTotal())
}

func TestOpsErrorLoggerMiddleware_RecoveredLogKeepsRealUpstreamAfterNormalizeAudit(t *testing.T) {
	resetOpsErrorLoggerStateForTest(t)
	opsErrorLogOnce.Do(func() {})
	opsErrorLogMu.Lock()
	opsErrorLogQueue = make(chan opsErrorLogJob, 4)
	opsErrorLogMu.Unlock()

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/messages", OpsErrorLoggerMiddleware(ops), func(c *gin.Context) {
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{
			{Kind: service.OpsUpstreamKindRequestNormalized, Message: "cc_environment_stripped"},
			{UpstreamStatusCode: http.StatusUnauthorized, Message: "invalid bearer token"},
		})
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(1), OpsErrorLogEnqueuedTotal())
	require.Equal(t, int64(1), OpsErrorLogQueueLength())

	select {
	case job := <-opsErrorLogQueue:
		require.NotNil(t, job.entry)
		require.Equal(t, http.StatusOK, job.entry.StatusCode)
		require.NotNil(t, job.entry.UpstreamStatusCode)
		require.Equal(t, http.StatusUnauthorized, *job.entry.UpstreamStatusCode)
		require.Contains(t, job.entry.ErrorMessage, "Recovered upstream error 401")
	default:
		t.Fatal("expected recovered upstream error log job")
	}
}
