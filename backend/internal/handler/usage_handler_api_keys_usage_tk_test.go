//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type batchUsageRepoCapture struct {
	service.UsageLogRepository
	start time.Time
	end   time.Time
	ids   []int64
}

func (s *batchUsageRepoCapture) GetBatchAPIKeyUsageStats(
	_ context.Context,
	apiKeyIDs []int64,
	startTime, endTime time.Time,
) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	s.ids = append([]int64(nil), apiKeyIDs...)
	s.start = startTime
	s.end = endTime
	out := make(map[int64]*usagestats.BatchAPIKeyUsageStats, len(apiKeyIDs))
	for _, id := range apiKeyIDs {
		out[id] = &usagestats.BatchAPIKeyUsageStats{APIKeyID: id, TotalActualCost: 1.25}
	}
	return out, nil
}

type batchUsageAPIKeyRepo struct {
	service.APIKeyRepository
}

func (batchUsageAPIKeyRepo) VerifyOwnership(_ context.Context, _ int64, apiKeyIDs []int64) ([]int64, error) {
	return apiKeyIDs, nil
}

func newDashboardAPIKeysUsageRouter(repo *batchUsageRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	apiKeyService := service.NewAPIKeyService(batchUsageAPIKeyRepo{}, nil, nil, nil, nil, nil, &config.Config{})
	usageService := service.NewUsageService(repo, nil, nil, nil)
	handler := NewUsageHandler(usageService, apiKeyService, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 16})
		c.Next()
	})
	router.POST("/usage/dashboard/api-keys-usage", handler.DashboardAPIKeysUsage)
	return router
}

func TestDashboardAPIKeysUsageDateRange(t *testing.T) {
	repo := &batchUsageRepoCapture{}
	router := newDashboardAPIKeysUsageRouter(repo)

	body := `{"api_key_ids":[11,12],"start_date":"2026-10-01","end_date":"2026-10-07","timezone":"UTC"}`
	req := httptest.NewRequest(http.MethodPost, "/usage/dashboard/api-keys-usage", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	wantStart, wantEnd, _, err := parseAPIKeyCalendarDateRange("2026-10-01", "2026-10-07", "UTC")
	require.NoError(t, err)
	require.True(t, repo.start.Equal(wantStart), "start=%v want=%v", repo.start, wantStart)
	require.True(t, repo.end.Equal(wantEnd), "end=%v want=%v", repo.end, wantEnd)
	require.Equal(t, []int64{11, 12}, repo.ids)

	var resp struct {
		Data struct {
			Stats map[string]struct {
				TotalActualCost float64 `json:"total_actual_cost"`
			} `json:"stats"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1.25, resp.Data.Stats["11"].TotalActualCost)
}

func TestDashboardAPIKeysUsageRejectsPartialDateRange(t *testing.T) {
	repo := &batchUsageRepoCapture{}
	router := newDashboardAPIKeysUsageRouter(repo)

	body := `{"api_key_ids":[11],"start_date":"2026-10-01"}`
	req := httptest.NewRequest(http.MethodPost, "/usage/dashboard/api-keys-usage", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDashboardAPIKeysUsageRejectsOversizedSpan(t *testing.T) {
	repo := &batchUsageRepoCapture{}
	router := newDashboardAPIKeysUsageRouter(repo)

	body := `{"api_key_ids":[11],"start_date":"2026-01-01","end_date":"2026-05-01","timezone":"UTC"}`
	req := httptest.NewRequest(http.MethodPost, "/usage/dashboard/api-keys-usage", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
