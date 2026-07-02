//go:build unit

package service

import (
	"context"
	"testing"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

type kiroPassiveSyncRepo struct {
	AccountRepository
	updates     map[string]any
	credentials map[string]any
}

func (r *kiroPassiveSyncRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updates = make(map[string]any, len(updates))
	for k, v := range updates {
		r.updates[k] = v
	}
	return nil
}

func (r *kiroPassiveSyncRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.credentials = shallowCopyMap(credentials)
	return nil
}

func TestBuildKiroUsageFromInfo_MapsCreditsAndTrial(t *testing.T) {
	info := &kiroproto.AccountInfo{
		SubscriptionTitle: "Kiro Pro",
		UsageCurrent:      300,
		UsageLimit:        1000,
		UsagePercent:      0.3, // 0-1 from RefreshAccountInfo
		NextResetDate:     "2026-07-01",
		TrialUsageCurrent: 5,
		TrialUsageLimit:   50,
		TrialUsagePercent: 0.1,
		TrialStatus:       "ACTIVE",
		TrialExpiresAt:    1893456000,
	}

	usage := buildKiroUsageFromInfo(info)
	if usage.Source != "active" {
		t.Fatalf("source = %q, want active", usage.Source)
	}
	ku := usage.KiroUsage
	if ku == nil {
		t.Fatal("KiroUsage is nil")
	}
	if ku.Current != 300 || ku.Limit != 1000 {
		t.Fatalf("credits = %v/%v, want 300/1000", ku.Current, ku.Limit)
	}
	if ku.Percent != 30 {
		t.Fatalf("percent = %v, want 30 (0-1 scaled to 0-100)", ku.Percent)
	}
	if ku.NextResetDate != "2026-07-01" {
		t.Fatalf("next_reset_date = %q, want 2026-07-01", ku.NextResetDate)
	}
	if ku.SubscriptionTitle != "Kiro Pro" {
		t.Fatalf("subscription_title = %q", ku.SubscriptionTitle)
	}
	if ku.Trial == nil {
		t.Fatal("trial is nil despite trial fields present")
	}
	if ku.Trial.Percent != 10 || ku.Trial.Status != "ACTIVE" {
		t.Fatalf("trial percent/status = %v/%q", ku.Trial.Percent, ku.Trial.Status)
	}
	if ku.Trial.ExpiresAt == nil || ku.Trial.ExpiresAt.Unix() != 1893456000 {
		t.Fatalf("trial expires_at = %v, want unix 1893456000", ku.Trial.ExpiresAt)
	}
}

func TestBuildKiroUsageFromInfo_NoTrialWhenAbsent(t *testing.T) {
	info := &kiroproto.AccountInfo{UsageLimit: 1000, UsageCurrent: 100, UsagePercent: 0.1}
	usage := buildKiroUsageFromInfo(info)
	if usage.KiroUsage == nil {
		t.Fatal("KiroUsage is nil")
	}
	if usage.KiroUsage.Trial != nil {
		t.Fatalf("trial should be nil when no trial fields, got %+v", usage.KiroUsage.Trial)
	}
}

func TestBuildPassiveKiroUsage_RoundTripFromExtra(t *testing.T) {
	svc := &AccountUsageService{}
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"kiro_usage_current":      float64(300),
			"kiro_usage_limit":        float64(1000),
			"kiro_usage_percent":      float64(30),
			"kiro_next_reset":         "2026-07-01",
			"kiro_subscription_title": "Kiro Pro",
			"kiro_trial_limit":        float64(50),
			"kiro_trial_percent":      float64(10),
			"kiro_trial_status":       "ACTIVE",
			"kiro_trial_expiry":       float64(1893456000),
			"kiro_usage_sampled_at":   "2026-06-27T00:00:00Z",
		},
	}

	usage := svc.buildPassiveKiroUsage(account)
	if usage.Source != "passive" {
		t.Fatalf("source = %q, want passive", usage.Source)
	}
	ku := usage.KiroUsage
	if ku == nil {
		t.Fatal("KiroUsage is nil after round-trip")
	}
	if ku.Current != 300 || ku.Limit != 1000 || ku.Percent != 30 {
		t.Fatalf("credits = %v/%v @ %v%%", ku.Current, ku.Limit, ku.Percent)
	}
	if ku.NextResetDate != "2026-07-01" || ku.SubscriptionTitle != "Kiro Pro" {
		t.Fatalf("reset/title = %q/%q", ku.NextResetDate, ku.SubscriptionTitle)
	}
	if ku.Trial == nil || ku.Trial.Status != "ACTIVE" || ku.Trial.ExpiresAt == nil {
		t.Fatalf("trial not reconstructed: %+v", ku.Trial)
	}
	if usage.UpdatedAt == nil {
		t.Fatal("UpdatedAt should come from kiro_usage_sampled_at")
	}
}

// TestGetKiroUsage_NonForcedReturnsPassiveNoUpstream pins the「仅按需刷新」
// invariant: a non-forced read (page load / auto-refresh) rebuilds from Extra and
// never calls RefreshAccountInfo. The unit build has no network, so reaching the
// upstream path would hang/panic — passing proves force=false never goes upstream.
func TestGetKiroUsage_NonForcedReturnsPassiveNoUpstream(t *testing.T) {
	svc := &AccountUsageService{cache: NewUsageCache()}
	account := &Account{
		ID:       42,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"kiro_usage_limit":   float64(1000),
			"kiro_usage_current": float64(250),
			"kiro_usage_percent": float64(25),
			"kiro_next_reset":    "2026-07-01",
		},
	}
	usage, err := svc.getKiroUsage(context.Background(), account, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if usage.Source != "passive" {
		t.Fatalf("source = %q, want passive (no upstream on force=false)", usage.Source)
	}
	if usage.KiroUsage == nil || usage.KiroUsage.Percent != 25 {
		t.Fatalf("expected passive credits rebuilt from Extra, got %+v", usage.KiroUsage)
	}
}

func TestBuildPassiveKiroUsage_EmptyWhenNoSample(t *testing.T) {
	svc := &AccountUsageService{}
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Extra: map[string]any{}}
	usage := svc.buildPassiveKiroUsage(account)
	if usage.KiroUsage != nil {
		t.Fatalf("KiroUsage should be nil when never sampled, got %+v", usage.KiroUsage)
	}
}

func TestSyncKiroActiveToPassive_ClearsStaleTrialAndSubscriptionKeys(t *testing.T) {
	repo := &kiroPassiveSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}

	svc.syncKiroActiveToPassive(context.Background(), 42, &UsageInfo{
		KiroUsage: &KiroUsageInfo{
			Current: 100,
			Limit:   1000,
			Percent: 10,
			// No subscription title, no trial, no next reset in the fresh snapshot.
		},
	})

	if repo.updates == nil {
		t.Fatal("expected UpdateExtra to be called")
	}
	if got := repo.updates["kiro_subscription_title"]; got != nil {
		t.Fatalf("kiro_subscription_title = %v, want nil clear", got)
	}
	for _, key := range []string{
		"kiro_trial_current",
		"kiro_trial_limit",
		"kiro_trial_percent",
		"kiro_trial_status",
		"kiro_trial_expiry",
		"kiro_next_reset",
	} {
		if got, ok := repo.updates[key]; !ok || got != nil {
			t.Fatalf("%s = %v (present=%v), want explicit nil clear", key, got, ok)
		}
	}
}

func TestPersistKiroProfileArnIfChanged_WritesResolvedArn(t *testing.T) {
	repo := &kiroPassiveSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	account := &Account{
		ID:          7,
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "tok"},
	}
	kiroAcct := &kiroproto.Account{ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/fresh"}

	svc.persistKiroProfileArnIfChanged(context.Background(), account, kiroAcct)

	if repo.credentials == nil {
		t.Fatal("expected UpdateCredentials")
	}
	if got := repo.credentials["profile_arn"]; got != "arn:aws:codewhisperer:us-east-1:1:profile/fresh" {
		t.Fatalf("profile_arn = %v, want fresh ARN", got)
	}
	if got := account.GetKiroProfileArn(); got != "arn:aws:codewhisperer:us-east-1:1:profile/fresh" {
		t.Fatalf("in-memory profile_arn = %q", got)
	}
}

func TestPersistKiroProfileArnIfChanged_SkipsWhenUnchanged(t *testing.T) {
	repo := &kiroPassiveSyncRepo{}
	svc := &AccountUsageService{accountRepo: repo}
	account := &Account{
		ID:       7,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/same",
		},
	}
	kiroAcct := &kiroproto.Account{ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/same"}

	svc.persistKiroProfileArnIfChanged(context.Background(), account, kiroAcct)

	if repo.credentials != nil {
		t.Fatalf("unexpected credential write: %+v", repo.credentials)
	}
}
