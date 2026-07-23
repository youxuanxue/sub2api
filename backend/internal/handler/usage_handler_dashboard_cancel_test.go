//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
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

type dashboardCancelAPIKeyRepo struct {
	service.APIKeyRepository
}

func (dashboardCancelAPIKeyRepo) VerifyOwnership(_ context.Context, _ int64, apiKeyIDs []int64) ([]int64, error) {
	return apiKeyIDs, nil
}

type dashboardCancelUsageRepo struct {
	service.UsageLogRepository
	err error
}

func (r dashboardCancelUsageRepo) GetBatchAPIKeyUsageStats(
	_ context.Context,
	_ []int64,
	_, _ time.Time,
) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	return nil, r.err
}

func runDashboardAPIKeysUsageErrorTest(t *testing.T, queryErr error) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	apiKeyService := service.NewAPIKeyService(
		dashboardCancelAPIKeyRepo{}, nil, nil, nil, nil, nil, &config.Config{},
	)
	usageService := service.NewUsageService(dashboardCancelUsageRepo{err: queryErr}, nil, nil, nil)
	h := NewUsageHandler(usageService, apiKeyService, nil, nil)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/usage/dashboard/api-keys-usage",
		bytes.NewBufferString(`{"api_key_ids":[9]}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})

	h.DashboardAPIKeysUsage(c)
	return recorder
}

func TestDashboardAPIKeysUsage_PostgresCallerCancellationReturns499(t *testing.T) {
	recorder := runDashboardAPIKeysUsageErrorTest(t, errors.New("pq: canceling statement due to user request"))

	require.Equal(t, middleware2.StatusClientClosedRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "CLIENT_CLOSED_REQUEST")
}

func TestDashboardAPIKeysUsage_DatabaseFailureRemains500(t *testing.T) {
	recorder := runDashboardAPIKeysUsageErrorTest(t, errors.New("database unavailable"))

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "database unavailable")
}
