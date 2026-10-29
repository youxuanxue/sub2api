package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type billingHTTPRepo struct {
	service.UsageLogRepository
	requestedUser int64
	calls         int
}

func (r *billingHTTPRepo) GetHistoricalBillingSnapshot(_ context.Context, id int64) (*usagestats.HistoricalBillingSnapshot, error) {
	r.requestedUser = id
	r.calls++
	return &usagestats.HistoricalBillingSnapshot{RegisteredAt: time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC), GeneratedAt: time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC), Timezone: "UTC", Rows: []usagestats.HistoricalBillingRow{
		{Date: "2026-06-04", Platform: "openai", Requests: id, ActualCost: "1.2345678901"},
		{Date: "2026-06-04", Platform: "", Requests: 1, ActualCost: "0.1"},
	}}, nil
}
func TestUS073_HistoricalBillingUserIsolationAndAdminParity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &billingHTTPRepo{}
	svc := service.NewUsageService(repo, nil, nil, nil)
	user := NewUsageHandler(svc, nil, nil, nil)
	admin := adminhandler.NewUsageHandler(svc, nil, nil, nil)
	router := gin.New()
	router.GET("/anonymous", user.HistoricalBilling)
	router.GET("/user", func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 16})
		user.HistoricalBilling(c)
	})
	// Admin auth is supplied by the production admin route group; this adapter
	// test exercises required user scope, shared filtering and response parity.
	router.GET("/admin", admin.HistoricalBilling)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	require.Equal(t, 401, get("/anonymous").Code)
	for _, path := range []string{"/user?user_id=17", "/user?model=hidden", "/user?start_ts=1", "/admin", "/admin?user_id=-1", "/admin?user_id=16&user_id=17", "/user?platform=a&platform=b"} {
		require.Equal(t, 400, get(path).Code, path)
	}
	require.Zero(t, repo.calls)
	userReply := get("/user?granularity=month&timezone=Asia%2FShanghai")
	require.Equal(t, 200, userReply.Code)
	require.Equal(t, int64(16), repo.requestedUser)
	require.Equal(t, "private, no-store", userReply.Header().Get("Cache-Control"))
	adminReply := get("/admin?user_id=16&granularity=month")
	require.Equal(t, 200, adminReply.Code)
	require.JSONEq(t, userReply.Body.String(), adminReply.Body.String())
	var body struct {
		Data usagestats.HistoricalBillingReport `json:"data"`
	}
	require.NoError(t, json.Unmarshal(userReply.Body.Bytes(), &body))
	require.Equal(t, "UTC", body.Data.Timezone)
	require.Equal(t, "1.3345678901", body.Data.Totals.ActualCost)
	require.NoError(t, json.Unmarshal(get("/user?platform=").Body.Bytes(), &body))
	require.Equal(t, "0.1", body.Data.Totals.ActualCost)
	require.Equal(t, int64(1), body.Data.Totals.Requests)
}
