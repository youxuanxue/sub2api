package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIUsageRefreshReplacesExhaustedSnapshotWithoutInference(t *testing.T) {
	account := &Account{ID: 283, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "workspace"},
		Extra: map[string]any{"codex_7d_used_percent": 100.0, "codex_7d_window_minutes": 10080,
			"codex_7d_reset_at":     time.Now().Add(18 * time.Hour).Format(time.RFC3339),
			"codex_5h_used_percent": 100.0, "codex_5h_window_minutes": 300,
			"codex_5h_reset_at":      time.Now().Add(time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{283: account}}
	tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "test-token"}}
	var paths []string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		require.Equal(t, http.MethodGet, r.Method)
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"quota unavailable"}`))
			return
		}
		if r.URL.Path == "/backend-api/wham/usage" {
			_, _ = w.Write([]byte(`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":1,"limit_window_seconds":604800,"reset_after_seconds":604000},"secondary_window":null}}`))
		} else {
			_, _ = w.Write([]byte(`{"credits":[]}`))
		}
	}))
	defer srv.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, tokens, nil), newQuotaRedirectingFactory(srv), nil)
	svc := &AccountUsageService{accountRepo: repo, openAIQuotaService: quota, cache: NewUsageCache()}
	before := svc.buildPassiveOpenAIUsage(context.Background(), account)
	require.Equal(t, 100.0, before.SevenDay.Utilization)
	require.True(t, before.SevenDay.UtilizationUnknown, "old quota must not be presented as current")
	require.Empty(t, paths, "list reads remain passive")
	usage, err := svc.getOpenAIUsage(context.Background(), account, false)
	require.NoError(t, err)
	require.Equal(t, 1.0, usage.SevenDay.Utilization)
	require.False(t, usage.SevenDay.UtilizationUnknown)
	require.Nil(t, usage.FiveHour, "an absent upstream window must clear the former exhausted window")
	require.Equal(t, 1.0, repo.extraUpdates[283]["codex_7d_used_percent"])
	require.Equal(t, 0, repo.extraUpdates[283]["codex_5h_window_minutes"])
	require.Equal(t, []string{"/backend-api/wham/usage", "/backend-api/wham/rate-limit-reset-credits"}, paths)
	require.Equal(t, usage.UpdatedAt, svc.buildPassiveOpenAIUsage(context.Background(), account).UpdatedAt)
	fail = true
	_, err = svc.getOpenAIUsage(context.Background(), account, true)
	require.Error(t, err, "a failed explicit query must not claim a successful refresh")
	require.Equal(t, 1.0, account.Extra["codex_7d_used_percent"])
}

func TestQuotaCardRefreshPersistsWindowsWithoutResetCreditDetails(t *testing.T) {
	account := &Account{ID: 283, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{283: account}}
	svc := &OpenAIQuotaService{accountRepo: repo}
	usage := &OpenAIQuotaUsage{FetchedAt: time.Now().Unix(), RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 1, LimitWindowSeconds: 604800, ResetAfterSeconds: 600000}}}
	require.NoError(t, svc.CacheCreditsSnapshot(context.Background(), 283, usage))
	require.Equal(t, 1.0, repo.extraUpdates[283]["codex_7d_used_percent"])
	require.NotContains(t, repo.extraUpdates[283], openaiQuotaResetCreditsKey)
	repo.extraUpdateErr = errors.New("write failed")
	require.Error(t, svc.CacheCreditsSnapshot(context.Background(), 283, usage))
}

func TestOpenAIUsageRefreshUnknownSnapshotAge(t *testing.T) {
	for _, timestamp := range []any{nil, "invalid"} {
		usage := &UsageInfo{}
		applyExtraToUsage(usage, map[string]any{
			"codex_usage_updated_at":  timestamp,
			"codex_7d_used_percent":   100.0,
			"codex_7d_window_minutes": 10080,
			"codex_7d_reset_at":       time.Now().Add(time.Hour).Format(time.RFC3339),
		}, time.Now())
		require.Nil(t, usage.UpdatedAt)
		require.NotNil(t, usage.SevenDay)
		require.True(t, usage.SevenDay.UtilizationUnknown)
	}
}

func TestQuotaCardRefreshKeepsShadowQuotaDimension(t *testing.T) {
	parentID := int64(282)
	account := &Account{ID: 283, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{283: account}}
	svc := &OpenAIQuotaService{accountRepo: repo}
	usage := &OpenAIQuotaUsage{FetchedAt: time.Now().Unix(),
		RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 1, LimitWindowSeconds: 604800, ResetAfterSeconds: 600000}},
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{MeteredFeature: "codex_bengalfox", RateLimit: &OpenAIRateLimit{
			PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 73, LimitWindowSeconds: 18000, ResetAfterSeconds: 1000},
			SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 81, LimitWindowSeconds: 604800, ResetAfterSeconds: 600000},
		}}},
	}
	require.NoError(t, svc.CacheCreditsSnapshot(context.Background(), 283, usage))
	require.Equal(t, 73.0, repo.extraUpdates[283]["codex_5h_used_percent"])
	require.Equal(t, 81.0, repo.extraUpdates[283]["codex_7d_used_percent"])
	require.NotContains(t, repo.extraUpdates, parentID)
	usage.AdditionalRateLimits[0].RateLimit.SecondaryWindow = nil
	require.NoError(t, svc.CacheCreditsSnapshot(context.Background(), 283, usage))
	require.Equal(t, 73.0, repo.extraUpdates[283]["codex_5h_used_percent"])
	require.Equal(t, 0, repo.extraUpdates[283]["codex_7d_window_minutes"], "missing shadow windows must also clear stale quota")
}
