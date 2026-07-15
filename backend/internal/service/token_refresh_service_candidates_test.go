package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

type tokenRefreshCandidateRepo struct {
	AccountRepository
	mu                    sync.Mutex
	accounts              []Account
	updatedCredentialIDs  []int64
	setErrorCalls         int
	setTempUnschedCalls   int
	clearTempCalls        int
	lastTempUnschedReason string
	listActiveCalls       int
}

func (r *tokenRefreshCandidateRepo) ListActive(context.Context) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listActiveCalls++
	return r.accounts, nil
}

func (r *tokenRefreshCandidateRepo) ListOAuthRefreshCandidatePage(_ context.Context, options OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	candidates := make([]Account, 0, len(r.accounts))
	now := time.Now()
	for _, account := range r.accounts {
		if account.ID <= options.AfterID {
			continue
		}
		refreshToken, _ := account.Credentials["refresh_token"].(string)
		inRetryCooldown := account.TempUnschedulableUntil != nil &&
			account.TempUnschedulableUntil.After(now) &&
			strings.HasPrefix(account.TempUnschedulableReason, "token refresh retry exhausted:")
		platformAllowed := false
		for _, platform := range options.Platforms {
			if account.Platform == platform {
				platformAllowed = true
				break
			}
		}
		if options.ActiveOnly && account.Status != StatusActive ||
			account.Type != AccountTypeOAuth ||
			!platformAllowed ||
			options.RequireRefreshToken && strings.TrimSpace(refreshToken) == "" ||
			options.ExcludeRetryCooldown && inRetryCooldown {
			continue
		}
		candidates = append(candidates, account)
		if len(candidates) == options.Limit {
			break
		}
	}
	page := &OAuthRefreshCandidatePage{Accounts: candidates, HasMore: len(candidates) == options.Limit}
	if len(candidates) > 0 {
		page.NextAfterID = candidates[len(candidates)-1].ID
	}
	return page, nil
}

func (r *tokenRefreshCandidateRepo) UpdateCredentials(_ context.Context, id int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredentialIDs = append(r.updatedCredentialIDs, id)
	return nil
}

func (r *tokenRefreshCandidateRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return nil
}

func (r *tokenRefreshCandidateRepo) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTempUnschedCalls++
	r.lastTempUnschedReason = reason
	return nil
}

func (r *tokenRefreshCandidateRepo) ClearTempUnschedulable(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearTempCalls++
	return nil
}

// isOAuthRefreshPlatform mirrors the production candidate filter. It delegates
// to engine.IsOAuthRefreshPlatform so this test stub and repository.
// ListOAuthRefreshCandidates' SQL `platform = ANY($1)` read the SAME single
// source of truth (engine.OAuthRefreshPlatforms()) and can never disagree.
func isOAuthRefreshPlatform(platform string) bool {
	return engine.IsOAuthRefreshPlatform(platform)
}

// TestRegisteredRefreshers_MatchOAuthRefreshSourceOfTruth welds the registered
// TokenRefresher set to engine.OAuthRefreshPlatforms() (the SQL source of
// truth). It is the structural kill for the R-001 silent-drop class:
//
//   - If a platform is in the source of truth (so its accounts are selected by
//     ListOAuthRefreshCandidates) but NO registered refresher will refresh it,
//     those accounts are fetched and silently skipped — they expire and 401 out
//     of the pool with no auto-recovery (exactly R-001). This test fails.
//   - If a refresher is registered for a platform NOT in the source of truth,
//     the SQL never feeds it accounts — a dead registration. This test fails.
//
// Both directions are caught by building the REAL registered refresher set via
// NewTokenRefreshService (not a re-listed copy) and probing each registered
// refresher with a per-platform OAuth probe account.
func TestRegisteredRefreshers_MatchOAuthRefreshSourceOfTruth(t *testing.T) {
	// Construction stores deps without dereferencing them (only cfg is read),
	// so nil collaborators are safe — CanRefresh inspects only Platform/Type.
	svc := NewTokenRefreshService(nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil)
	require.NotEmpty(t, svc.refreshers, "no refreshers registered")

	covered := make(map[string]bool)
	// Probe across every scheduling platform (the superset) plus a bogus one, so
	// we detect refreshers that accept a platform outside the source of truth.
	probeUniverse := append(engine.AllSchedulingPlatforms(), "definitely-not-a-platform")
	for _, platform := range probeUniverse {
		probe := &Account{Platform: platform, Type: AccountTypeOAuth}
		for _, r := range svc.refreshers {
			if r.CanRefresh(probe) {
				covered[platform] = true
			}
		}
	}

	got := make([]string, 0, len(covered))
	for p := range covered {
		got = append(got, p)
	}
	require.ElementsMatch(t, engine.OAuthRefreshPlatforms(), got,
		"the registered TokenRefresher set must cover EXACTLY engine.OAuthRefreshPlatforms() — "+
			"a platform in the source of truth with no refresher (or vice versa) is the R-001 silent-drop class")
}

type tokenRefreshTestRefresher struct {
	err error
}

func (r *tokenRefreshTestRefresher) CanRefresh(*Account) bool { return true }

func (r *tokenRefreshTestRefresher) NeedsRefresh(*Account, time.Duration) bool { return true }

func (r *tokenRefreshTestRefresher) Refresh(context.Context, *Account) (map[string]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{"access_token": "new-access-token", "refresh_token": "new-refresh-token"}, nil
}

func TestTokenRefreshService_ProcessRefreshUsesOAuthRefreshCandidates(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	repo := &tokenRefreshCandidateRepo{
		accounts: []Account{
			{
				ID:          1,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:          2,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: map[string]any{},
			},
			{
				ID:          3,
				Platform:    PlatformGemini,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:                      4,
				Platform:                PlatformAntigravity,
				Type:                    AccountTypeOAuth,
				Status:                  StatusActive,
				Credentials:             map[string]any{"refresh_token": "refresh-token"},
				TempUnschedulableUntil:  &future,
				TempUnschedulableReason: "token refresh retry exhausted: network timeout",
			},
			{
				ID:          5,
				Platform:    "other",
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			// TK platforms 6/7: kiro + grok depend on background refresh (no
			// on-demand path; grok tokens expire ~1h). They MUST be candidates.
			{
				ID:          6,
				Platform:    PlatformKiro,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:          7,
				Platform:    PlatformGrok,
				Type:        AccountTypeOAuth,
				Status:      StatusActive,
				Credentials: map[string]any{"refresh_token": "refresh-token"},
			},
			{
				ID:                      8,
				Platform:                PlatformAntigravity,
				Type:                    AccountTypeOAuth,
				Status:                  StatusActive,
				Credentials:             map[string]any{"refresh_token": "refresh-token"},
				Extra:                   map[string]any{"privacy_mode": AntigravityPrivacySet},
				TempUnschedulableUntil:  &future,
				TempUnschedulableReason: "OAuth 401: unauthorized",
			},
		},
	}
	svc := &TokenRefreshService{
		accountRepo:    repo,
		candidatePager: repo,
		registrations: []tokenRefreshRegistration{
			{platform: PlatformOpenAI, refresher: &tokenRefreshTestRefresher{}},
			{platform: PlatformGemini, refresher: &tokenRefreshTestRefresher{}},
			{platform: PlatformAntigravity, refresher: &tokenRefreshTestRefresher{}},
		},
		refreshPolicy: DefaultBackgroundRefreshPolicy(),
		cfg:           &config.TokenRefreshConfig{RefreshBeforeExpiryHours: 1, MaxRetries: 1},
	}

	svc.processRefresh()

	require.Zero(t, repo.listActiveCalls, "TokenRefreshService should not use the broad active-account query")
	require.Equal(t, []int64{1, 6, 7, 8}, repo.updatedCredentialIDs,
		"kiro/grok OAuth accounts must remain background-refresh candidates; "+
			"antigravity with non-retry temp-unschedulable (OAuth 401) must also refresh")
	require.Equal(t, 1, repo.clearTempCalls, "successful refresh should clear the OAuth 401 temp-unschedulable state")
}

func TestTokenRefreshService_RefreshFailureDoesNotCallPrivacy(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "retry exhausted", err: errors.New("temporary upstream timeout")},
		{name: "non retryable", err: errors.New("invalid_grant: token revoked")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &tokenRefreshCandidateRepo{}
			svc := &TokenRefreshService{
				accountRepo:   repo,
				refreshPolicy: DefaultBackgroundRefreshPolicy(),
				cfg:           &config.TokenRefreshConfig{MaxRetries: 1, RetryBackoffSeconds: 0},
				privacyClientFactory: func(string) (*req.Client, error) {
					t.Fatalf("privacy client factory must not be called on refresh failure")
					return nil, errors.New("unexpected privacy call")
				},
			}
			account := &Account{
				ID:       11,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"access_token":  "old-access-token",
					"refresh_token": "refresh-token",
				},
			}

			err := svc.refreshWithRetry(context.Background(), account, &tokenRefreshTestRefresher{err: tt.err}, nil, time.Hour)

			require.Error(t, err)
			if isNonRetryableRefreshError(tt.err) {
				require.Equal(t, 1, repo.setErrorCalls)
				require.Zero(t, repo.setTempUnschedCalls)
			} else {
				require.Zero(t, repo.setErrorCalls)
				require.Equal(t, 1, repo.setTempUnschedCalls)
				require.True(t, strings.HasPrefix(repo.lastTempUnschedReason, "token refresh retry exhausted:"))
			}
		})
	}
}
