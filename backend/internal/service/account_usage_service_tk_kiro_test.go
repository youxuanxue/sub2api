//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

type kiroPassiveSyncRepo struct {
	AccountRepository
	updates     map[string]any
	credentials map[string]any
}

type kiroUsageRefreshRepo struct {
	AccountRepository
	account                *Account
	updateCredentialsCalls int
	updateExtraCalls       int
	clearErrorCalls        int
}

func (r *kiroUsageRefreshRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func (r *kiroUsageRefreshRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.updateCredentialsCalls++
	r.account.Credentials = shallowCopyMap(credentials)
	return nil
}

func (r *kiroUsageRefreshRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updateExtraCalls++
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for key, value := range updates {
		r.account.Extra[key] = value
	}
	return nil
}

func (r *kiroUsageRefreshRepo) ClearError(_ context.Context, _ int64) error {
	r.clearErrorCalls++
	r.account.Status = StatusActive
	r.account.ErrorMessage = ""
	return nil
}

type kiroUsageRefreshExecutorStub struct {
	refreshErr   error
	accessToken  string
	refreshCalls int
}

func (e *kiroUsageRefreshExecutorStub) CanRefresh(account *Account) bool {
	return account != nil && account.Platform == PlatformKiro && account.Type == AccountTypeOAuth
}

func (e *kiroUsageRefreshExecutorStub) NeedsRefresh(_ *Account, _ time.Duration) bool {
	return false
}

func (e *kiroUsageRefreshExecutorStub) Refresh(_ context.Context, account *Account) (map[string]any, error) {
	e.refreshCalls++
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	return MergeCredentials(account.Credentials, map[string]any{
		"access_token": e.accessToken,
		"expires_at":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}), nil
}

func (e *kiroUsageRefreshExecutorStub) CacheKey(account *Account) string {
	return fmt.Sprintf("kiro-usage-test:%d", account.ID)
}

func newKiroUsageRefreshTestAccount(status, errorMessage string, expiresAt time.Time) *Account {
	return &Account{
		ID:           42,
		Name:         "kiro-test",
		Platform:     PlatformKiro,
		Type:         AccountTypeOAuth,
		Status:       status,
		Schedulable:  status == StatusActive,
		ErrorMessage: errorMessage,
		Credentials: map[string]any{
			"access_token":  "old-access",
			"refresh_token": "refresh-token",
			"expires_at":    strconv.FormatInt(expiresAt.Unix(), 10),
			"profile_arn":   "arn:aws:codewhisperer:us-east-1:1:profile/test",
		},
		Extra: map[string]any{},
	}
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
		Bonuses: []kiroproto.KiroBonusInfo{
			{
				Code:      "WELCOME500",
				Label:     "Welcome Bonus",
				Current:   120,
				Limit:     500,
				Percent:   24,
				Status:    "ACTIVE",
				ExpiresAt: 1893456000,
			},
		},
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
	if len(ku.Bonuses) != 1 {
		t.Fatalf("bonuses = %+v, want one entry", ku.Bonuses)
	}
	if ku.Bonuses[0].Code != "WELCOME500" || ku.Bonuses[0].Limit != 500 {
		t.Fatalf("bonus = %+v", ku.Bonuses[0])
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
			"kiro_bonuses":            `[{"code":"WELCOME500","label":"Welcome Bonus","current":120,"limit":500,"percent":24,"status":"ACTIVE","expires_at":"2026-07-01T00:00:00Z"}]`,
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
	if len(ku.Bonuses) != 1 || ku.Bonuses[0].Code != "WELCOME500" {
		t.Fatalf("bonuses not reconstructed: %+v", ku.Bonuses)
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

func TestGetKiroUsage_ExpiredQuotaAccountRefreshesTokenWithoutReenabling(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(
		StatusError,
		"Payment required (402): You have reached the limit.",
		time.Now().Add(-time.Minute),
	)
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(got *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			if got.AccessToken == "old-access" {
				return nil, errors.New(`GetUsageLimits: HTTP 403: {"message":"Invalid token"}`)
			}
			if got.AccessToken != "new-access" {
				t.Fatalf("usage access token = %q, want old or refreshed token", got.AccessToken)
			}
			return &kiroproto.AccountInfo{UsageCurrent: 10000, UsageLimit: 10000, UsagePercent: 1}, nil
		},
	}

	usage, err := svc.getUsageForAccount(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getUsageForAccount() error = %v", err)
	}
	if executor.refreshCalls != 1 || fetchCalls != 2 {
		t.Fatalf("refresh/fetch calls = %d/%d, want 1/2", executor.refreshCalls, fetchCalls)
	}
	if usage.Source != "active" || usage.KiroUsage == nil || usage.KiroUsage.Percent != 100 {
		t.Fatalf("usage = %+v, want active exhausted snapshot", usage)
	}
	if account.Status != StatusError || account.Schedulable {
		t.Fatalf("account status/schedulable = %q/%v, quota error must remain blocked", account.Status, account.Schedulable)
	}
	if repo.clearErrorCalls != 0 {
		t.Fatalf("ClearError calls = %d, quota error must not be cleared by usage", repo.clearErrorCalls)
	}
}

func TestGetKiroUsage_SuccessDoesNotClearRecoverableAccountError(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(
		StatusError,
		"token refresh failed: previous attempt",
		time.Now().Add(-time.Minute),
	)
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(_ *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			return &kiroproto.AccountInfo{UsageCurrent: 250, UsageLimit: 1000, UsagePercent: 0.25}, nil
		},
	}

	usage, err := svc.getUsageForAccount(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getUsageForAccount() error = %v", err)
	}
	if usage.Source != "active" || usage.Error != "" {
		t.Fatalf("usage = %+v, want successful active snapshot", usage)
	}
	if executor.refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, successful usage must not rotate credentials", executor.refreshCalls)
	}
	if repo.clearErrorCalls != 0 || account.Status != StatusError || account.Schedulable {
		t.Fatalf("clear/status/schedulable = %d/%q/%v, usage must not restore scheduling", repo.clearErrorCalls, account.Status, account.Schedulable)
	}
}

func TestGetKiroUsage_NearExpiryUsesStillValidTokenWithoutRefresh(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(StatusActive, "", time.Now().Add(2*time.Minute))
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{refreshErr: errors.New("unexpected refresh")}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(got *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			if got.AccessToken != "old-access" {
				t.Fatalf("usage access token = %q, want still-valid stored token", got.AccessToken)
			}
			return &kiroproto.AccountInfo{UsageCurrent: 100, UsageLimit: 1000, UsagePercent: 0.1}, nil
		},
	}

	usage, err := svc.getKiroUsage(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getKiroUsage() error = %v", err)
	}
	if executor.refreshCalls != 0 || fetchCalls != 1 {
		t.Fatalf("refresh/fetch calls = %d/%d, want 0/1", executor.refreshCalls, fetchCalls)
	}
	if usage.Source != "active" || usage.Error != "" {
		t.Fatalf("usage = %+v, want active snapshot from still-valid token", usage)
	}
}

func TestGetKiroUsage_InvalidTokenForceRefreshesOnceAndRetries(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(StatusActive, "", time.Now().Add(time.Hour))
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(got *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			if got.AccessToken == "old-access" {
				return nil, errors.New(`GetUsageLimits: HTTP 403 from https://codewhisperer.us-east-1.amazonaws.com: {"message":"Invalid token"}`)
			}
			return &kiroproto.AccountInfo{UsageCurrent: 250, UsageLimit: 1000, UsagePercent: 0.25}, nil
		},
	}

	usage, err := svc.getKiroUsage(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getKiroUsage() error = %v", err)
	}
	if executor.refreshCalls != 1 || fetchCalls != 2 {
		t.Fatalf("refresh/fetch calls = %d/%d, want 1/2", executor.refreshCalls, fetchCalls)
	}
	if usage.Error != "" || usage.KiroUsage == nil || usage.KiroUsage.Percent != 25 {
		t.Fatalf("usage = %+v, want successful retry snapshot", usage)
	}
}

func TestIsKiroUsageTokenAuthError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"plain invalid token", errors.New(`HTTP 403: {"message":"Invalid token"}`), true},
		{"invalid bearer sentence", errors.New("HTTP 403: The bearer token included in the request is invalid"), true},
		{"unauthorized", errors.New("HTTP 401: Unauthorized"), true},
		{"non-token forbidden", errors.New("HTTP 403: Organization forbidden"), false},
		{"quota", errors.New("HTTP 402: You have reached the limit"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isKiroUsageTokenAuthError(tc.err); got != tc.want {
				t.Fatalf("isKiroUsageTokenAuthError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRedactKiroUsageError_CamelCaseCredentials(t *testing.T) {
	err := errors.New(`refresh failed: {"accessToken":"at-secret","refreshToken":"rt-secret","clientSecret":"cs-secret"}`)
	got := redactKiroUsageError(err)
	for _, secret := range []string{"at-secret", "rt-secret", "cs-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted error contains %q: %s", secret, got)
		}
	}
	for _, key := range []string{"accessToken", "refreshToken", "clientSecret"} {
		if !strings.Contains(got, `"`+key+`":"***"`) {
			t.Fatalf("redacted error missing %s marker: %s", key, got)
		}
	}
}

func TestGetKiroUsage_SecondInvalidTokenDoesNotLoop(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(StatusActive, "", time.Now().Add(time.Hour))
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(_ *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			return nil, errors.New(`GetUsageLimits: HTTP 403: {"message":"Invalid token"}`)
		},
	}

	usage, err := svc.getKiroUsage(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getKiroUsage() error = %v", err)
	}
	if executor.refreshCalls != 1 || fetchCalls != 2 {
		t.Fatalf("refresh/fetch calls = %d/%d, want one refresh and one retry", executor.refreshCalls, fetchCalls)
	}
	if usage.Error == "" || !strings.Contains(usage.Error, "Invalid token") {
		t.Fatalf("usage error = %q, want final Invalid token", usage.Error)
	}
}

func TestGetKiroUsage_NonToken403DoesNotRefresh(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(StatusActive, "", time.Now().Add(time.Hour))
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(_ *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			return nil, errors.New(`GetUsageLimits: HTTP 403: {"message":"Organization forbidden"}`)
		},
	}

	usage, err := svc.getKiroUsage(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getKiroUsage() error = %v", err)
	}
	if executor.refreshCalls != 0 || fetchCalls != 1 {
		t.Fatalf("refresh/fetch calls = %d/%d, want 0/1", executor.refreshCalls, fetchCalls)
	}
	if usage.Error == "" {
		t.Fatal("expected degraded usage error")
	}
}

func TestGetKiroUsage_RefreshFailureDoesNotClearRecoverableAccountError(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(
		StatusError,
		"token refresh failed: previous attempt",
		time.Now().Add(-time.Minute),
	)
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{refreshErr: errors.New("invalid_grant")}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, nil),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(_ *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			return nil, errors.New(`GetUsageLimits: HTTP 403: {"message":"Invalid token"}`)
		},
	}

	usage, err := svc.getUsageForAccount(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getUsageForAccount() error = %v", err)
	}
	if executor.refreshCalls != 1 || fetchCalls != 1 {
		t.Fatalf("refresh/fetch calls = %d/%d, want 1/1", executor.refreshCalls, fetchCalls)
	}
	if usage.Source != "passive" || !strings.Contains(usage.Error, "Kiro OAuth token refresh failed") {
		t.Fatalf("usage = %+v, want passive refresh error", usage)
	}
	if strings.Contains(usage.Error, "invalid_grant") {
		t.Fatalf("usage error leaks refresh detail: %q", usage.Error)
	}
	if repo.clearErrorCalls != 0 || account.Status != StatusError {
		t.Fatalf("clear calls/status = %d/%q, degraded usage must not clear error", repo.clearErrorCalls, account.Status)
	}
}

func TestGetKiroUsage_RefreshLockHeldDoesNotRetryOldToken(t *testing.T) {
	account := newKiroUsageRefreshTestAccount(StatusError, "token refresh failed", time.Now().Add(-time.Minute))
	repo := &kiroUsageRefreshRepo{account: account}
	executor := &kiroUsageRefreshExecutorStub{accessToken: "new-access"}
	cache := &refreshAPICacheStub{lockResult: false}
	var fetchCalls int
	svc := &AccountUsageService{
		accountRepo:              repo,
		cache:                    NewUsageCache(),
		oauthRefreshAPI:          NewOAuthRefreshAPI(repo, cache),
		kiroOAuthRefreshExecutor: executor,
		kiroUsageFetcher: func(_ *kiroproto.Account) (*kiroproto.AccountInfo, error) {
			fetchCalls++
			return nil, errors.New(`GetUsageLimits: HTTP 403: {"message":"Invalid token"}`)
		},
	}

	usage, err := svc.getUsageForAccount(context.Background(), account, true)
	if err != nil {
		t.Fatalf("getUsageForAccount() error = %v", err)
	}
	if executor.refreshCalls != 0 || fetchCalls != 1 {
		t.Fatalf("refresh/fetch calls = %d/%d, want one rejected usage call and no unlocked refresh", executor.refreshCalls, fetchCalls)
	}
	if !strings.Contains(usage.Error, "Kiro OAuth token refresh failed") {
		t.Fatalf("usage error = %q, want generic refresh failure", usage.Error)
	}
	if repo.clearErrorCalls != 0 {
		t.Fatalf("ClearError calls = %d, want 0", repo.clearErrorCalls)
	}
}

func TestBuildPassiveKiroUsage_EmptyWhenNoSample(t *testing.T) {
	svc := &AccountUsageService{}
	account := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Extra: map[string]any{}}
	usage := svc.buildPassiveKiroUsage(account)
	if usage.KiroUsage != nil {
		t.Fatalf("KiroUsage should be nil when never sampled, got %+v", usage.KiroUsage)
	}
	if usage.UpdatedAt != nil {
		t.Fatalf("UpdatedAt = %v, want nil when never sampled", usage.UpdatedAt)
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
		"kiro_bonuses",
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

func TestBuildKiroUpstreamQuota_IncludesTrialAndBonuses(t *testing.T) {
	now := time.Now()
	expires := now.Add(24 * time.Hour)
	usage := &UsageInfo{
		Source:    "active",
		UpdatedAt: &now,
		KiroUsage: &KiroUsageInfo{
			Current:           300,
			Limit:             1000,
			Percent:           30,
			SubscriptionTitle: "KIRO POWER",
			Trial: &KiroTrialInfo{
				Current: 5,
				Limit:   50,
				Percent: 10,
				Status:  "ACTIVE",
			},
			Bonuses: []KiroBonusInfo{
				{
					Code:      "WELCOME500",
					Label:     "Welcome Bonus",
					Current:   120,
					Limit:     500,
					Percent:   24,
					Status:    "ACTIVE",
					ExpiresAt: &expires,
				},
			},
		},
	}

	info := buildKiroUpstreamQuota(usage)
	if info == nil || info.State != "observed" {
		t.Fatalf("upstream quota = %+v", info)
	}
	if len(info.Credits) != 3 {
		t.Fatalf("credits len = %d, want 3", len(info.Credits))
	}
	if info.Credits[0].Key != "kiro_credits" {
		t.Fatalf("first credit key = %q", info.Credits[0].Key)
	}
	if info.Credits[1].Key != "kiro_trial" {
		t.Fatalf("second credit key = %q", info.Credits[1].Key)
	}
	if info.Credits[2].Key != "kiro_bonus_welcome500" {
		t.Fatalf("third credit key = %q", info.Credits[2].Key)
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
