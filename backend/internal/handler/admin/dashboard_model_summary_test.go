package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelSummaryRepo struct {
	service.UsageLogRepository
	calls      int
	filters    usagestats.UsageLogFilters
	start, end time.Time
}

func (r *modelSummaryRepo) GetModelStatsWithUsageFiltersBySource(_ context.Context, start, end time.Time, filters usagestats.UsageLogFilters, source string) ([]usagestats.ModelStat, error) {
	r.calls++
	r.filters, r.start, r.end = filters, start, end
	// Distinct results make missing cache dimensions observable in the HTTP response.
	cost := float64(r.calls)
	return []usagestats.ModelStat{{Model: filters.Model, TotalTokens: 400, Requests: 2, ActualCost: cost}}, nil
}

func TestModelSummaryFilterAndCacheIsolation(t *testing.T) {
	resetDashboardReadCachesForTest()
	t.Cleanup(resetDashboardReadCachesForTest)
	repo := &modelSummaryRepo{}
	handler := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/models", handler.GetModelStats)
	base := "/models?user_id=16&start_ts=1792461600000&end_ts=1792548000000&model_source=requested&native_compaction_v2=true"
	for _, tc := range []struct {
		query     string
		wantCost  float64
		wantCalls int
	}{
		{"&model=a&billing_mode=token", 1, 1},
		{"&model=a&billing_mode=token", 1, 1},
		{"&model=b&billing_mode=token", 2, 2},
		{"&model=b&billing_mode=image", 3, 3},
		{"&model=a&billing_mode=token", 1, 3},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, base+tc.query, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var body struct {
			Data struct {
				Models []usagestats.ModelStat `json:"models"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, tc.wantCost, body.Data.Models[0].ActualCost)
		require.Equal(t, int64(400), body.Data.Models[0].TotalTokens)
		require.Equal(t, tc.wantCalls, repo.calls)
	}
	require.Equal(t, int64(16), repo.filters.UserID)
	require.Equal(t, "b", repo.filters.Model)
	require.Equal(t, "image", repo.filters.BillingMode)
	require.NotNil(t, repo.filters.NativeCompactionV2)
	require.True(t, *repo.filters.NativeCompactionV2)
	require.Equal(t, int64(1792461600000), repo.start.UnixMilli())
	require.Equal(t, int64(1792548000000), repo.end.UnixMilli())
}
