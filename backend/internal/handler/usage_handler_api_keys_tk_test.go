package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type apiKeyStatsRepoCapture struct {
	service.UsageLogRepository
	start   time.Time
	end     time.Time
	filters usagestats.UsageLogFilters
	stats   []usagestats.APIKeyStat
}

func (s *apiKeyStatsRepoCapture) GetAPIKeyStatsWithUsageFilters(
	ctx context.Context,
	startTime, endTime time.Time,
	filters usagestats.UsageLogFilters,
) ([]usagestats.APIKeyStat, error) {
	s.start = startTime
	s.end = endTime
	s.filters = filters
	return s.stats, nil
}

func newDashboardAPIKeysRouter(repo *apiKeyStatsRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	usageSvc := service.NewUsageService(repo, nil, nil, nil)
	handler := NewUsageHandler(usageSvc, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 16})
		c.Next()
	})
	router.GET("/usage/dashboard/api-keys", handler.DashboardAPIKeys)
	return router
}

func TestDashboardAPIKeysDefaultSevenDays(t *testing.T) {
	repo := &apiKeyStatsRepoCapture{
		stats: []usagestats.APIKeyStat{
			{APIKeyID: 11, APIKeyName: "prod-key", Requests: 3, TotalTokens: 900, Cost: 1.2, ActualCost: 1.0},
		},
	}
	router := newDashboardAPIKeysRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-keys?timezone=Asia/Shanghai", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int64(16), repo.filters.UserID)

	wantStart, wantEnd := apiKeyDailyUsageRange(7, "Asia/Shanghai")
	require.True(t, repo.start.Equal(wantStart), "start=%v want=%v", repo.start, wantStart)
	require.True(t, repo.end.Equal(wantEnd), "end=%v want=%v", repo.end, wantEnd)

	var body struct {
		Data struct {
			Days      int    `json:"days"`
			StartDate string `json:"start_date"`
			EndDate   string `json:"end_date"`
			APIKeys   []struct {
				APIKeyID   int64   `json:"api_key_id"`
				APIKeyName string  `json:"api_key_name"`
				ActualCost float64 `json:"actual_cost"`
			} `json:"api_keys"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 7, body.Data.Days)
	require.Equal(t, wantStart.Format("2006-01-02"), body.Data.StartDate)
	require.Equal(t, wantEnd.AddDate(0, 0, -1).Format("2006-01-02"), body.Data.EndDate)
	require.Len(t, body.Data.APIKeys, 1)
	require.Equal(t, int64(11), body.Data.APIKeys[0].APIKeyID)
	require.Equal(t, "prod-key", body.Data.APIKeys[0].APIKeyName)
	require.Equal(t, 1.0, body.Data.APIKeys[0].ActualCost)
}

func TestDashboardAPIKeysThirtyDays(t *testing.T) {
	repo := &apiKeyStatsRepoCapture{}
	router := newDashboardAPIKeysRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-keys?days=30&timezone=UTC", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	wantStart, wantEnd := apiKeyDailyUsageRange(30, "UTC")
	require.True(t, repo.start.Equal(wantStart), "start=%v want=%v", repo.start, wantStart)
	require.True(t, repo.end.Equal(wantEnd), "end=%v want=%v", repo.end, wantEnd)
}

func TestDashboardAPIKeysDateRange(t *testing.T) {
	repo := &apiKeyStatsRepoCapture{}
	router := newDashboardAPIKeysRouter(repo)

	req := httptest.NewRequest(
		http.MethodGet,
		"/usage/dashboard/api-keys?start_date=2026-10-01&end_date=2026-10-07&timezone=UTC",
		nil,
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	wantStart, wantEnd, wantDays, err := parseAPIKeyCalendarDateRange("2026-10-01", "2026-10-07", "UTC")
	require.NoError(t, err)
	require.Equal(t, 7, wantDays)
	require.True(t, repo.start.Equal(wantStart), "start=%v want=%v", repo.start, wantStart)
	require.True(t, repo.end.Equal(wantEnd), "end=%v want=%v", repo.end, wantEnd)

	var body struct {
		Data struct {
			Days      int    `json:"days"`
			StartDate string `json:"start_date"`
			EndDate   string `json:"end_date"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 7, body.Data.Days)
	require.Equal(t, "2026-10-01", body.Data.StartDate)
	require.Equal(t, "2026-10-07", body.Data.EndDate)
}

func TestDashboardAPIKeysRejectsInvalidDays(t *testing.T) {
	repo := &apiKeyStatsRepoCapture{}
	router := newDashboardAPIKeysRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-keys?days=14", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardAPIKeysRejectsPartialDateRange(t *testing.T) {
	repo := &apiKeyStatsRepoCapture{}
	router := newDashboardAPIKeysRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/usage/dashboard/api-keys?start_date=2026-10-01", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestParseAPIKeyCalendarDateRangeSpringForwardDay(t *testing.T) {
	// America/New_York spring-forward 2026-03-08 is a 23h civil day; wall-clock
	// Hours/24 would yield 0 and incorrectly reject a valid single-day window.
	start, end, days, err := parseAPIKeyCalendarDateRange("2026-03-08", "2026-03-08", "America/New_York")
	require.NoError(t, err)
	require.Equal(t, 1, days)
	require.True(t, end.After(start))
	require.Equal(t, "2026-03-08", start.Format("2006-01-02"))
	require.Equal(t, "2026-03-09", end.Format("2006-01-02"))
}
