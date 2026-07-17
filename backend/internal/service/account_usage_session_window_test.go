package service

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"
)

// sessionWindowSyncRepo 记录 syncActiveToPassive 触发的所有写操作。
type sessionWindowSyncRepo struct {
	AccountRepository

	mu                sync.Mutex
	extraUpdates      []map[string]any
	sessionWindowEnds []sessionWindowEndCall
}

type sessionWindowEndCall struct {
	AccountID int64
	End       time.Time
}

func (r *sessionWindowSyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]any, len(updates))
	for k, v := range updates {
		copied[k] = v
	}
	r.extraUpdates = append(r.extraUpdates, copied)
	return nil
}

func (r *sessionWindowSyncRepo) UpdateSessionWindowEnd(_ context.Context, id int64, end time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionWindowEnds = append(r.sessionWindowEnds, sessionWindowEndCall{AccountID: id, End: end})
	return nil
}

func TestEstimateSetupTokenUsage_ExpiredWindowZeroes(t *testing.T) {
	t.Parallel()

	past := time.Now().Add(-2 * time.Hour)
	svc := &AccountUsageService{}
	info := svc.estimateSetupTokenUsage(&Account{
		SessionWindowEnd: &past,
		Extra: map[string]any{
			"session_window_utilization": 0.53,
		},
	})

	if info.FiveHour == nil {
		t.Fatal("expected non-nil FiveHour info")
	}
	if info.FiveHour.Utilization != 0 {
		t.Fatalf("expected Utilization=0 for expired window, got %v", info.FiveHour.Utilization)
	}
	if info.FiveHour.ResetsAt != nil {
		t.Fatalf("expected ResetsAt=nil for expired window, got %v", info.FiveHour.ResetsAt)
	}
	if info.FiveHour.RemainingSeconds != 0 {
		t.Fatalf("expected RemainingSeconds=0 for expired window, got %v", info.FiveHour.RemainingSeconds)
	}
}

func TestEstimateSetupTokenUsage_ActiveWindowPreservesUtilization(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(3 * time.Hour)
	svc := &AccountUsageService{}
	info := svc.estimateSetupTokenUsage(&Account{
		SessionWindowEnd: &future,
		Extra: map[string]any{
			"session_window_utilization": 0.53,
		},
	})

	if info.FiveHour == nil {
		t.Fatal("expected non-nil FiveHour info")
	}
	if info.FiveHour.Utilization != 53 {
		t.Fatalf("expected Utilization=53, got %v", info.FiveHour.Utilization)
	}
	if info.FiveHour.ResetsAt == nil || !info.FiveHour.ResetsAt.Equal(future) {
		t.Fatalf("expected ResetsAt=%v, got %v", future, info.FiveHour.ResetsAt)
	}
	if info.FiveHour.RemainingSeconds <= 0 {
		t.Fatalf("expected positive RemainingSeconds, got %v", info.FiveHour.RemainingSeconds)
	}
}

func TestSyncActiveToPassive_WritesFiveHourSessionWindowEnd(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	resetsAt := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)
	svc.syncActiveToPassive(context.Background(), 42, &UsageInfo{
		FiveHour: &UsageProgress{
			Utilization: 53,
			ResetsAt:    &resetsAt,
		},
	})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.sessionWindowEnds) != 1 {
		t.Fatalf("expected 1 UpdateSessionWindowEnd call, got %d", len(repo.sessionWindowEnds))
	}
	call := repo.sessionWindowEnds[0]
	if call.AccountID != 42 {
		t.Fatalf("expected AccountID=42, got %d", call.AccountID)
	}
	if !call.End.Equal(resetsAt) {
		t.Fatalf("expected End=%v, got %v", resetsAt, call.End)
	}
}

func TestSyncActiveToPassive_SkipsSessionWindowEndWhenResetMissing(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	svc.syncActiveToPassive(context.Background(), 99, &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 10},
	})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.sessionWindowEnds) != 0 {
		t.Fatalf("expected no UpdateSessionWindowEnd calls when ResetsAt is nil, got %d", len(repo.sessionWindowEnds))
	}
}

// TestSyncActiveToPassive_PersistsSevenDaySonnet pins that a manual「查询」(active)
// result's 7d-Sonnet sub-window is written back to Extra. Without this the only
// source of 7d-S (the active /api/oauth/usage endpoint) is lost on the next passive
// load, so the admin "用量窗口" column can never show 7d-S after a refresh.
func TestSyncActiveToPassive_PersistsSevenDaySonnet(t *testing.T) {
	t.Parallel()

	repo := &sessionWindowSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	sonnetReset := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	svc.syncActiveToPassive(context.Background(), 7, &UsageInfo{
		FiveHour:       &UsageProgress{Utilization: 17},
		SevenDay:       &UsageProgress{Utilization: 4},
		SevenDaySonnet: &UsageProgress{Utilization: 6, ResetsAt: &sonnetReset},
	})

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.extraUpdates) != 1 {
		t.Fatalf("expected 1 UpdateExtra call, got %d", len(repo.extraUpdates))
	}
	u := repo.extraUpdates[0]
	if v, ok := u["passive_usage_7d_sonnet_utilization"].(float64); !ok || math.Abs(v-6.0/100) > 1e-9 {
		t.Fatalf("expected passive_usage_7d_sonnet_utilization=%v, got %v", 6.0/100, u["passive_usage_7d_sonnet_utilization"])
	}
	if v, ok := u["passive_usage_7d_sonnet_reset"].(int64); !ok || v != sonnetReset.Unix() {
		t.Fatalf("expected passive_usage_7d_sonnet_reset=%d, got %v", sonnetReset.Unix(), u["passive_usage_7d_sonnet_reset"])
	}
}

// TestGetPassiveUsage_RebuildsSevenDaySonnet pins the read side: once 7d-S has been
// persisted (by a prior active query), a passive load rebuilds it so it survives
// page refresh / 自动刷新.
func TestGetPassiveUsage_RebuildsSevenDaySonnet(t *testing.T) {
	t.Parallel()

	sonnetReset := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	acct := Account{
		ID:       8,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"passive_usage_7d_utilization":        0.04,
			"passive_usage_7d_reset":              float64(time.Now().Add(5 * 24 * time.Hour).Unix()),
			"passive_usage_7d_sonnet_utilization": 0.06,
			"passive_usage_7d_sonnet_reset":       float64(sonnetReset.Unix()),
		},
	}
	cache := NewUsageCache()
	// Preseed window-stats cache so addWindowStats never touches a nil usageLogRepo.
	cache.windowStatsCache.Store(acct.ID, &windowStatsCache{stats: &WindowStats{}, timestamp: time.Now()})
	svc := &AccountUsageService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{acct}},
		cache:       cache,
	}

	info, err := svc.GetPassiveUsage(context.Background(), acct.ID)
	if err != nil {
		t.Fatalf("GetPassiveUsage error: %v", err)
	}
	if info.SevenDay == nil {
		t.Fatal("expected SevenDay (overall 7d) to rebuild")
	}
	if info.SevenDaySonnet == nil {
		t.Fatal("expected SevenDaySonnet to be rebuilt from passive extra, got nil")
	}
	if math.Abs(info.SevenDaySonnet.Utilization-6) > 1e-9 {
		t.Fatalf("expected SevenDaySonnet.Utilization=6, got %v", info.SevenDaySonnet.Utilization)
	}
	if info.SevenDaySonnet.ResetsAt == nil || info.SevenDaySonnet.ResetsAt.Unix() != sonnetReset.Unix() {
		t.Fatalf("expected SevenDaySonnet.ResetsAt unix=%d, got %v", sonnetReset.Unix(), info.SevenDaySonnet.ResetsAt)
	}
}
