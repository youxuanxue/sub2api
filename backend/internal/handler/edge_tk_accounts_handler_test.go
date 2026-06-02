//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type edgeAccountsListerStub struct {
	accounts     []service.Account
	err          error
	lastPlatform string
	lastStatus   string
}

func (s *edgeAccountsListerStub) ListAccounts(_ context.Context, _, _ int, platform, _, status, _ string, _ int64, _, _, _ string) ([]service.Account, int64, error) {
	s.lastPlatform = platform
	s.lastStatus = status
	return s.accounts, int64(len(s.accounts)), s.err
}

func performEdgeAccountsRequest(t *testing.T, h *EdgeAccountsHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/edge/accounts"+query, nil)
	h.ListAccounts(c)
	return w
}

// richAccount returns an account populated with BOTH sensitive credentials and
// the non-sensitive fields the DTO is allowed to expose, so the leak assertion
// is meaningful.
func richAccount() service.Account {
	mult := 1.5
	tierID := int64(7)
	expires := time.Now().Add(24 * time.Hour)
	return service.Account{
		ID:       42,
		Name:     "edge-acct-1",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"api_key":       "sk-super-secret-key",
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"base_url":      "https://api-us1.tokenkey.dev",
		},
		Extra: map[string]any{
			"window_cost_limit": 50.0,
			"max_sessions":      30,
			"base_rpm":          28,
		},
		Concurrency:    8,
		Priority:       3,
		RateMultiplier: &mult,
		Schedulable:    true,
		ExpiresAt:      &expires,
		CreatedAt:      time.Now(),
		TierID:         &tierID,
		Groups:         []*service.Group{{ID: 1, Name: "default"}, {ID: 2, Name: "vip"}},
	}
}

func TestEdgeAccountsHandler_ReturnsSanitizedAccounts(t *testing.T) {
	stub := &edgeAccountsListerStub{accounts: []service.Account{richAccount()}}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformAnthropic, stub.lastPlatform)
	// MUST list all statuses (status filter empty) to mirror the edge admin page.
	require.Equal(t, "", stub.lastStatus)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "anthropic", env.Data.Platform)
	require.NotZero(t, env.Data.TS)
	require.Len(t, env.Data.Accounts, 1)

	got := env.Data.Accounts[0]
	require.Equal(t, int64(42), got.ID)
	require.Equal(t, "edge-acct-1", got.Name)
	require.Equal(t, 8, got.Concurrency)
	require.Equal(t, 1.5, got.RateMultiplier)
	require.Equal(t, 50.0, got.WindowCostLimit)
	require.Equal(t, 30, got.MaxSessions)
	require.Equal(t, 28, got.BaseRPM)
	require.Equal(t, []string{"default", "vip"}, got.Groups)
	require.NotNil(t, got.TierID)
	require.Equal(t, int64(7), *got.TierID)
}

// TestEdgeAccountsHandler_NeverLeaksCredentials is the load-bearing security
// assertion: the raw response bytes must not contain ANY credential value or key.
func TestEdgeAccountsHandler_NeverLeaksCredentials(t *testing.T) {
	stub := &edgeAccountsListerStub{accounts: []service.Account{richAccount()}}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)

	body := w.Body.String()
	for _, forbidden := range []string{
		"sk-super-secret-key", "at-secret", "rt-secret", // credential values
		"api_key", "access_token", "refresh_token", "base_url", // credential keys
		"credentials", // the map itself must not appear
	} {
		require.NotContainsf(t, body, forbidden,
			"response leaked credential-related token %q: %s", forbidden, body)
	}
}

// ---- runtime-gauge enrichment ----

type fakeConcReader struct{ m map[int64]int }

func (f fakeConcReader) GetAccountConcurrencyBatch(_ context.Context, _ []int64) (map[int64]int, error) {
	return f.m, nil
}

type fakeSessReader struct{ m map[int64]int }

func (f fakeSessReader) GetActiveSessionCountBatch(_ context.Context, _ []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	return f.m, nil
}

type fakeRPMReader struct{ m map[int64]int }

func (f fakeRPMReader) GetRPMBatch(_ context.Context, _ []int64) (map[int64]int, error) {
	return f.m, nil
}

type fakeUsageReader struct {
	today   map[int64]*service.WindowStats
	wcost   float64
	passive *service.UsageInfo
}

func (f fakeUsageReader) GetAccountWindowStats(_ context.Context, _ int64, _ time.Time) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{StandardCost: f.wcost}, nil
}

func (f fakeUsageReader) GetTodayStatsBatch(_ context.Context, _ []int64) (map[int64]*service.WindowStats, error) {
	return f.today, nil
}

func (f fakeUsageReader) GetPassiveUsage(_ context.Context, _ int64) (*service.UsageInfo, error) {
	return f.passive, nil
}

func richOAuthAccount() service.Account {
	return service.Account{
		ID:       7,
		Name:     "edge-oauth-1",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Extra: map[string]any{
			"window_cost_limit": 600.0,
			"max_sessions":      150,
			"base_rpm":          56,
		},
		Concurrency: 12,
		Priority:    5,
		Schedulable: true,
		CreatedAt:   time.Now(),
	}
}

func TestEdgeAccountsHandler_EnrichesRuntimeGauges(t *testing.T) {
	stub := &edgeAccountsListerStub{accounts: []service.Account{richOAuthAccount()}}
	h := NewEdgeAccountsHandler(
		stub,
		fakeConcReader{m: map[int64]int{7: 3}},
		fakeSessReader{m: map[int64]int{7: 4}},
		fakeRPMReader{m: map[int64]int{7: 9}},
		fakeUsageReader{
			today:   map[int64]*service.WindowStats{7: {Requests: 80, Tokens: 65_900_000, Cost: 36.53, UserCost: 36.53}},
			wcost:   36.53,
			passive: &service.UsageInfo{Source: "passive", FiveHour: &service.UsageProgress{Utilization: 2}, SevenDay: &service.UsageProgress{Utilization: 4}},
		},
	)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	got := env.Data.Accounts[0]

	require.Equal(t, 3, got.CurrentConcurrency)
	require.NotNil(t, got.ActiveSessions)
	require.Equal(t, 4, *got.ActiveSessions)
	require.NotNil(t, got.CurrentRPM)
	require.Equal(t, 9, *got.CurrentRPM)
	require.NotNil(t, got.CurrentWindowCost)
	require.Equal(t, 36.53, *got.CurrentWindowCost)
	require.NotNil(t, got.TodayStats)
	require.Equal(t, int64(80), got.TodayStats.Requests)
	require.Equal(t, int64(65_900_000), got.TodayStats.Tokens)
	require.Equal(t, 36.53, got.TodayStats.Cost)
	require.Equal(t, 36.53, got.TodayStats.UserCost)

	require.NotNil(t, got.Usage)
	require.Equal(t, "passive", got.Usage.Source)
	require.NotNil(t, got.Usage.FiveHour)
	require.Equal(t, 2.0, got.Usage.FiveHour.Utilization)
	require.NotNil(t, got.Usage.SevenDay)
	require.Equal(t, 4.0, got.Usage.SevenDay.Utilization)
}

func TestEdgeAccountsHandler_RejectsUnknownPlatform(t *testing.T) {
	h := NewEdgeAccountsHandler(&edgeAccountsListerStub{}, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=bogus")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEdgeAccountsHandler_DefaultsToAnthropic(t *testing.T) {
	stub := &edgeAccountsListerStub{}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformAnthropic, stub.lastPlatform)
}

func TestEdgeAccountsHandler_ListError(t *testing.T) {
	h := NewEdgeAccountsHandler(&edgeAccountsListerStub{err: errors.New("db down")}, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestEdgeAccountsHandler_NilReader(t *testing.T) {
	h := NewEdgeAccountsHandler(nil, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.False(t, strings.Contains(w.Body.String(), `"accounts"`))
}
