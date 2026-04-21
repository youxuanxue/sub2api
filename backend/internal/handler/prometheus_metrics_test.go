package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPrometheusMetrics_ExposesLabeledSeries(t *testing.T) {
	t.Parallel()

	firstToken := int64(120)
	service.ObserveFusionHTTPRequest("openai", "gpt-4o", 200, 400*time.Millisecond, &firstToken)
	service.RecordFusionAccountFailure("openai", 7, "5xx")
	service.SetFusionAccountPoolSize("openai", "active", 2)
	service.RecordFusionUsageBillingApplyError("apply_failed")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/metrics", PrometheusMetrics)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "sub2api_http_requests_total")
	require.Contains(t, body, `platform="openai"`)
	require.Contains(t, body, "sub2api_http_request_duration_seconds_bucket")
	require.Contains(t, body, "sub2api_first_token_seconds_bucket")
	require.Contains(t, body, "sub2api_account_pool_size")
	require.Contains(t, body, "sub2api_account_failure_total")
	require.Contains(t, body, "sub2api_usage_billing_apply_errors_total")
}
