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

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type edgeAccountsListerStub struct {
	accounts     []service.Account
	err          error
	lastPlatform string
	lastStatus   string
	lastGroupID  int64
}

func (s *edgeAccountsListerStub) ListAccounts(_ context.Context, _, _ int, platform, _, status, _ string, groupID int64, _, _, _ string) ([]service.Account, int64, error) {
	s.lastPlatform = platform
	s.lastStatus = status
	s.lastGroupID = groupID
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
	notes := "operator remark for edge-acct-1"
	return service.Account{
		ID:       42,
		Name:     "edge-acct-1",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Notes:    &notes,
		Credentials: map[string]any{
			"api_key":       "sk-super-secret-key",
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"base_url":      "https://api-us1.tokenkey.dev",
		},
		Extra: map[string]any{
			"max_sessions": 30,
			"base_rpm":     28,
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
	require.Equal(t, 30, got.MaxSessions)
	require.Equal(t, 28, got.BaseRPM)
	require.Equal(t, []string{"default", "vip"}, got.Groups)
	require.NotNil(t, got.TierID)
	require.Equal(t, int64(7), *got.TierID)
	// The 备注 is surfaced so the overview's name cell matches the admin page.
	require.NotNil(t, got.Notes)
	require.Equal(t, "operator remark for edge-acct-1", *got.Notes)
}

// TestEdgeAccountsHandler_NeverLeaksCredentials is the load-bearing security
// assertion: the raw response bytes must not contain ANY credential value or key.
// It also seeds an active per-class cooldown so the curated model_rate_limits
// projection is exercised alongside the leak check — a future credential-key rename
// must not silently start leaking through the one Extra field that IS serialized.
func TestEdgeAccountsHandler_NeverLeaksCredentials(t *testing.T) {
	acct := richAccount()
	acct.Extra["model_rate_limits"] = map[string]any{
		"anthropic:class:sonnet": map[string]any{
			"rate_limit_reset_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			"rate_limited_at":     time.Now().Format(time.RFC3339),
			"reason":              "anthropic_unified_window_exceeded",
		},
	}
	stub := &edgeAccountsListerStub{accounts: []service.Account{acct}}
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
	// The curated cooldown projection IS present (so a credential-key rename can't
	// silently swallow it without this assertion noticing).
	require.Contains(t, body, "model_rate_limits")
	require.Contains(t, body, "anthropic:class:sonnet")
}

// TestEdgeAccountsHandler_ForwardsActiveModelRateLimits verifies the curated
// active-only model_rate_limits projection: an edge OAuth account with an active
// sonnet unified-window cooldown surfaces ONLY that scope (with reset + reason);
// a lapsed cooldown (opus) is filtered out. This is the visibility surface that
// lets the prod overview see an edge's per-class window without it sitting null on
// the prod mirror account.
func TestEdgeAccountsHandler_ForwardsActiveModelRateLimits(t *testing.T) {
	acct := richOAuthAccount()
	resetAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	limitedAt := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Second)
	acct.Extra["model_rate_limits"] = map[string]any{
		"anthropic:class:sonnet": map[string]any{
			"rate_limit_reset_at": resetAt.Format(time.RFC3339),
			"rate_limited_at":     limitedAt.Format(time.RFC3339),
			"reason":              "anthropic_unified_window_exceeded",
		},
		// Lapsed (reset in the past) → must be filtered out (active-only).
		"anthropic:class:opus": map[string]any{
			"rate_limit_reset_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}

	stub := &edgeAccountsListerStub{accounts: []service.Account{acct}}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	got := env.Data.Accounts[0]

	require.Len(t, got.ModelRateLimits, 1, "only the active sonnet cooldown survives")
	entry, ok := got.ModelRateLimits["anthropic:class:sonnet"]
	require.True(t, ok, "the active sonnet scope must be present")
	require.NotContains(t, got.ModelRateLimits, "anthropic:class:opus", "the lapsed opus cooldown is filtered out")
	require.NotNil(t, entry.RateLimitResetAt)
	require.WithinDuration(t, resetAt, *entry.RateLimitResetAt, time.Second)
	require.NotNil(t, entry.RateLimitedAt)
	require.WithinDuration(t, limitedAt, *entry.RateLimitedAt, time.Second)
	require.Equal(t, "anthropic_unified_window_exceeded", entry.Reason)
}

// TestEdgeAccountsHandler_OmitsModelRateLimitsWhenInactive verifies the field is
// nil (and the JSON omits it) when the account has no Extra / no active cooldown.
func TestEdgeAccountsHandler_OmitsModelRateLimitsWhenInactive(t *testing.T) {
	// No Extra at all.
	bare := richOAuthAccount()
	bare.Extra = nil
	stub := &edgeAccountsListerStub{accounts: []service.Account{bare}}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), "model_rate_limits", "field must be omitted when no Extra")

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	require.Nil(t, env.Data.Accounts[0].ModelRateLimits)
}

// TestActiveModelRateLimits_FiltersByActivity is a focused unit on the service-layer
// enumerator: future kept, past dropped, malformed reset skipped (no panic), nil
// account safe.
func TestActiveModelRateLimits_FiltersByActivity(t *testing.T) {
	t.Run("nil account is safe", func(t *testing.T) {
		var a *service.Account
		require.Nil(t, a.ActiveModelRateLimits(time.Now()))
	})

	t.Run("nil Extra is safe", func(t *testing.T) {
		a := &service.Account{}
		require.Nil(t, a.ActiveModelRateLimits(time.Now()))
	})

	now := time.Now()
	a := &service.Account{Extra: map[string]any{
		"model_rate_limits": map[string]any{
			"anthropic:class:sonnet": map[string]any{
				"rate_limit_reset_at": now.Add(time.Hour).Format(time.RFC3339),
				"reason":              "anthropic_unified_window_exceeded",
			},
			"anthropic:class:opus": map[string]any{
				"rate_limit_reset_at": now.Add(-time.Hour).Format(time.RFC3339),
			},
			"anthropic:class:haiku": map[string]any{
				"rate_limit_reset_at": "not-a-timestamp",
			},
		},
	}}
	active := a.ActiveModelRateLimits(now)
	require.Len(t, active, 1)
	c, ok := active["anthropic:class:sonnet"]
	require.True(t, ok)
	require.True(t, c.RateLimitResetAt.After(now))
	require.Equal(t, "anthropic_unified_window_exceeded", c.Reason)
}

// TestEdgeAccountsHandler_GroupScopeCaller verifies the v2 precise-correspondence
// filter: group_scope=caller narrows the read to the authenticated caller key's
// group (direct key), the whole pool for a universal key, and is a no-op (full
// inventory, groupID 0) when the param is absent — so the standalone overview is
// unchanged.
func TestEdgeAccountsHandler_GroupScopeCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(99)
	directKey := &service.APIKey{
		ID:          7,
		GroupID:     &groupID,
		RoutingMode: service.RoutingModeDirect,
		Group:       &service.Group{ID: 99, Name: "default"},
	}
	universalKey := &service.APIKey{ID: 8, RoutingMode: service.RoutingModeUniversal}

	cases := []struct {
		name        string
		query       string
		key         *service.APIKey
		wantGroupID int64
		wantGroup   string
	}{
		{"direct key + group_scope=caller → filter by its group", "?group_scope=caller", directKey, 99, "default"},
		{"universal key → no group filter (whole pool)", "?group_scope=caller", universalKey, 0, ""},
		{"no group_scope → full inventory (standalone unchanged)", "", directKey, 0, ""},
		{"group_scope=caller but no caller key in ctx → full inventory", "?group_scope=caller", nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &edgeAccountsListerStub{accounts: []service.Account{richAccount()}}
			h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/edge/accounts"+tc.query, nil)
			if tc.key != nil {
				c.Set(middleware.EdgeCallerAPIKeyCtxKey, tc.key)
			}
			h.ListAccounts(c)

			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, tc.wantGroupID, stub.lastGroupID)

			var env struct {
				Data edgeAccountsResponse `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
			require.Equal(t, tc.wantGroup, env.Data.Group)
		})
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
	passive *service.UsageInfo
}

func (f fakeUsageReader) GetTodayStatsBatch(_ context.Context, _ []int64) (map[int64]*service.WindowStats, error) {
	return f.today, nil
}

func (f fakeUsageReader) GetPassiveUsage(_ context.Context, _ int64) (*service.UsageInfo, error) {
	return f.passive, nil
}

func (f fakeUsageReader) GetPassiveUsageBatch(_ context.Context, accountIDs []int64) map[int64]*service.UsageInfo {
	// The service adapter owns passive capability filtering; this fake mirrors
	// only the fan-out contract for handler tests.
	usage := make(map[int64]*service.UsageInfo)
	if f.passive == nil {
		return usage
	}
	for _, id := range accountIDs {
		usage[id] = f.passive
	}
	return usage
}

func richOAuthAccount() service.Account {
	return service.Account{
		ID:       7,
		Name:     "edge-oauth-1",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Extra: map[string]any{
			"max_sessions": 150,
			"base_rpm":     56,
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
			passive: &service.UsageInfo{Source: "passive", FiveHour: &service.UsageProgress{Utilization: 2}, SevenDay: &service.UsageProgress{Utilization: 4}, SevenDaySonnet: &service.UsageProgress{Utilization: 6}},
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
	require.NotNil(t, got.Usage.SevenDaySonnet, "anthropic account must forward the 7d Sonnet sub-window to the edge overview")
	require.Equal(t, 6.0, got.Usage.SevenDaySonnet.Utilization)
}

// OpenAI OAuth (codex) accounts must also carry the passive 5h/7d usage windows
// on the prod cross-edge overview, matching the edge's own admin page. Before the
// gate widened to IsOpenAIOAuth, only anthropic rows passed GetPassiveUsage and
// OpenAI cells rendered "-" even though the edge page showed the 5h/7d bars.
func TestEdgeAccountsHandler_PopulatesOpenAIOAuthUsageWindows(t *testing.T) {
	openaiAcct := service.Account{
		ID:          9,
		Name:        "edge-openai-1",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		CreatedAt:   time.Now(),
	}
	stub := &edgeAccountsListerStub{accounts: []service.Account{openaiAcct}}
	h := NewEdgeAccountsHandler(
		stub,
		fakeConcReader{m: map[int64]int{9: 1}},
		nil,
		nil,
		fakeUsageReader{
			passive: &service.UsageInfo{Source: "passive", FiveHour: &service.UsageProgress{Utilization: 12}, SevenDay: &service.UsageProgress{Utilization: 34}},
		},
	)
	w := performEdgeAccountsRequest(t, h, "?platform=openai")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	got := env.Data.Accounts[0]

	require.NotNil(t, got.Usage, "openai oauth account must carry passive 5h/7d windows on the edge overview")
	require.Equal(t, "passive", got.Usage.Source)
	require.NotNil(t, got.Usage.FiveHour)
	require.Equal(t, 12.0, got.Usage.FiveHour.Utilization)
	require.NotNil(t, got.Usage.SevenDay)
	require.Equal(t, 34.0, got.Usage.SevenDay.Utilization)
}

func TestEdgeAccountsHandler_PopulatesKiroUsageWindows(t *testing.T) {
	sampledAt := time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC)
	kiroAcct := service.Account{
		ID:          11,
		Name:        "edge-kiro-1",
		Platform:    service.PlatformKiro,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		CreatedAt:   time.Now(),
	}
	stub := &edgeAccountsListerStub{accounts: []service.Account{kiroAcct}}
	h := NewEdgeAccountsHandler(
		stub,
		fakeConcReader{m: map[int64]int{11: 1}},
		nil,
		nil,
		fakeUsageReader{
			passive: &service.UsageInfo{
				Source:    "passive",
				UpdatedAt: &sampledAt,
				KiroUsage: &service.KiroUsageInfo{
					Current:           300,
					Limit:             1000,
					Percent:           30,
					NextResetDate:     "2026-07-01",
					SubscriptionTitle: "Kiro Pro",
					Trial: &service.KiroTrialInfo{
						Percent: 10,
						Status:  "ACTIVE",
					},
				},
			},
		},
	)
	w := performEdgeAccountsRequest(t, h, "?platform=kiro")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	got := env.Data.Accounts[0]

	require.NotNil(t, got.Usage)
	require.Equal(t, "passive", got.Usage.Source)
	require.Equal(t, sampledAt, *got.Usage.UpdatedAt)
	require.NotNil(t, got.Usage.Kiro)
	require.Equal(t, 300.0, got.Usage.Kiro.Current)
	require.Equal(t, 1000.0, got.Usage.Kiro.Limit)
	require.Equal(t, 30.0, got.Usage.Kiro.Percent)
	require.Equal(t, "2026-07-01", got.Usage.Kiro.NextResetDate)
	require.Equal(t, "Kiro Pro", got.Usage.Kiro.SubscriptionTitle)
	require.Equal(t, 10.0, got.Usage.Kiro.TrialPercent)
	require.Equal(t, "ACTIVE", got.Usage.Kiro.TrialStatus)
}

func TestEdgeAccountsHandler_PassesLocalWindowAdaptersToUsageBatch(t *testing.T) {
	acct := service.Account{
		ID:          12,
		Name:        "edge-newapi-vertex-1",
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeServiceAccount,
		ChannelType: 41,
		Status:      service.StatusActive,
		Schedulable: true,
		CreatedAt:   time.Now(),
	}
	stub := &edgeAccountsListerStub{accounts: []service.Account{acct}}
	h := NewEdgeAccountsHandler(
		stub,
		fakeConcReader{m: map[int64]int{12: 1}},
		nil,
		nil,
		fakeUsageReader{
			passive: &service.UsageInfo{
				Source:   "passive",
				FiveHour: &service.UsageProgress{WindowStats: &service.WindowStats{Tokens: 2048}},
				SevenDay: &service.UsageProgress{WindowStats: &service.WindowStats{Tokens: 8192}},
				UpstreamQuota: &service.UpstreamQuotaInfo{
					Provider: "newapi",
					State:    "unsupported",
				},
			},
		},
	)
	w := performEdgeAccountsRequest(t, h, "?platform=newapi")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Len(t, env.Data.Accounts, 1)
	got := env.Data.Accounts[0]

	require.NotNil(t, got.Usage, "newapi local-window adapter must reach edge overview passive batch")
	require.NotNil(t, got.Usage.FiveHour)
	require.NotNil(t, got.Usage.FiveHour.WindowStats)
	require.Equal(t, int64(2048), got.Usage.FiveHour.WindowStats.Tokens)
	require.NotNil(t, got.Usage.SevenDay)
	require.NotNil(t, got.Usage.SevenDay.WindowStats)
	require.Equal(t, int64(8192), got.Usage.SevenDay.WindowStats.Tokens)
	require.NotNil(t, got.Usage.UpstreamQuota)
	require.Equal(t, "newapi", got.Usage.UpstreamQuota.Provider)
	require.Equal(t, "unsupported", got.Usage.UpstreamQuota.State)
}

func TestEdgeAccountsHandler_RejectsUnknownPlatform(t *testing.T) {
	h := NewEdgeAccountsHandler(&edgeAccountsListerStub{}, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=bogus")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

// platform=all is the overview default: it must map to an EMPTY ListAccounts
// platform filter so every platform's accounts come back in one read.
func TestEdgeAccountsHandler_AllPlatformQueriesEveryPlatform(t *testing.T) {
	stub := &edgeAccountsListerStub{accounts: []service.Account{richAccount()}}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=all")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "", stub.lastPlatform) // "all" → no platform filter

	var env struct {
		Data edgeAccountsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "all", env.Data.Platform) // echoes the requested sentinel
}

// A concrete non-anthropic platform must be accepted and passed through verbatim
// (newapi/kiro are first-class edge platforms now, not just anthropic).
func TestEdgeAccountsHandler_AcceptsNewAPIPlatform(t *testing.T) {
	stub := &edgeAccountsListerStub{}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "?platform=newapi")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, service.PlatformNewAPI, stub.lastPlatform)
}

// Bare request (no ?platform=) defaults to the "all" sentinel → empty filter.
func TestEdgeAccountsHandler_DefaultsToAll(t *testing.T) {
	stub := &edgeAccountsListerStub{}
	h := NewEdgeAccountsHandler(stub, nil, nil, nil, nil)
	w := performEdgeAccountsRequest(t, h, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "", stub.lastPlatform)
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
