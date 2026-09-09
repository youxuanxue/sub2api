//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type cursorRefreshTestUpstream struct {
	HTTPUpstream
	do func(*http.Request, string, int64, int) (*http.Response, error)
}

func (u *cursorRefreshTestUpstream) Do(req *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.do(req, proxy, id, concurrency)
}

func cursorRefreshTestToken(expires time.Time) string {
	return "test." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expires.Unix()))) + ".signature"
}

type cursorRefreshTestRepo struct {
	AccountRepository
	account       *Account
	beforePersist func()
	persistError  error
	persisted     int
	failed        int
}

func (r *cursorRefreshTestRepo) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.account
	copy.Credentials = shallowCopyMap(copy.Credentials)
	return &copy, nil
}
func (r *cursorRefreshTestRepo) ListOAuthRefreshCandidatePage(_ context.Context, opts OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	for _, platform := range opts.Platforms {
		if platform == PlatformNewAPI && r.account.ID > opts.AfterID {
			return &OAuthRefreshCandidatePage{Accounts: []Account{*r.account}, NextAfterID: r.account.ID}, nil
		}
	}
	return &OAuthRefreshCandidatePage{}, nil
}
func (r *cursorRefreshTestRepo) UpdateCursorOAuthCredentialsIfUnchanged(_ context.Context, _ int64, expected map[string]any, proxy *int64, credentials map[string]any, expires time.Time) (bool, error) {
	if r.beforePersist != nil {
		r.beforePersist()
	}
	if r.persistError != nil {
		return false, r.persistError
	}
	if !reflect.DeepEqual(r.account.Credentials, expected) || !reflect.DeepEqual(r.account.ProxyID, proxy) {
		return false, nil
	}
	r.persisted++
	r.account.Credentials = shallowCopyMap(credentials)
	r.account.ExpiresAt = &expires
	return true, nil
}
func (r *cursorRefreshTestRepo) SetCursorOAuthRefreshFailureIfUnchanged(_ context.Context, _ int64, expected map[string]any, _ *int64, _ string, _ *time.Time) (bool, error) {
	if !reflect.DeepEqual(r.account.Credentials, expected) {
		return false, nil
	}
	r.failed++
	return true, nil
}
func (r *cursorRefreshTestRepo) ClearTempUnschedulable(context.Context, int64) error { return nil }

func TestCursorBackgroundRefreshUsesSharedAPIAndPreservesPause(t *testing.T) {
	for _, pauseDuringRefresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("pause=%v", pauseDuringRefresh), func(t *testing.T) {
			account := cursorCandidateAccount("composer-2.5")
			account.Status, account.Schedulable = StatusActive, true
			oldExpiry := time.Now().Add(5 * time.Minute)
			account.ExpiresAt = &oldExpiry
			account.Credentials["api_key"] = cursorRefreshTestToken(oldExpiry)
			mapping := account.Credentials["model_mapping"]
			freshExpiry := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Second)
			freshToken := cursorRefreshTestToken(freshExpiry)
			repo := &cursorRefreshTestRepo{account: account}
			if pauseDuringRefresh {
				repo.beforePersist = func() { repo.account.Schedulable = false }
			}
			calls := 0
			upstream := &cursorRefreshTestUpstream{do: func(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
				calls++
				require.Equal(t, account.ID, id)
				require.Equal(t, "https://api2.cursor.sh/oauth/token", req.URL.String())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q}`, freshToken)))}, nil
			}}
			svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{TokenRefresh: config.TokenRefreshConfig{RefreshBeforeExpiryHours: 1}}, nil)
			defer svc.Stop()
			svc.SetCursorRefreshUpstream(upstream)
			svc.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil))
			svc.processRefreshContext(t.Context())
			require.Equal(t, 1, calls)
			require.Equal(t, 1, repo.persisted)
			require.Equal(t, freshToken, repo.account.Credentials["api_key"])
			require.Equal(t, freshExpiry.Unix(), repo.account.ExpiresAt.Unix())
			require.Equal(t, !pauseDuringRefresh, repo.account.Schedulable)
			require.Equal(t, mapping, repo.account.Credentials["model_mapping"])
			svc.processRefreshContext(t.Context())
			require.Equal(t, 1, calls, "fresh credentials must not refresh again")
		})
	}
}

func TestCursorRefreshCannotOverwriteReauthorization(t *testing.T) {
	account := cursorCandidateAccount("composer-2.5")
	account.Credentials["api_key"] = cursorRefreshTestToken(time.Now().Add(time.Hour))
	repo := &cursorRefreshTestRepo{account: account}
	snapshot, err := repo.GetByID(t.Context(), account.ID)
	require.NoError(t, err)
	repo.account.Credentials["api_key"] = "reauthorized-secret"
	creds := shallowCopyMap(snapshot.Credentials)
	creds["api_key"] = cursorRefreshTestToken(time.Now().Add(24 * time.Hour))
	require.ErrorIs(t, persistAccountCredentials(t.Context(), repo, snapshot, creds), errRefreshSkipped)
	svc := &TokenRefreshService{accountRepo: repo}
	require.ErrorIs(t, svc.recordCursorRefreshFailure(t.Context(), snapshot, fmt.Errorf("invalid_grant"), nil), errRefreshSkipped)
	require.Zero(t, repo.persisted)
	require.Zero(t, repo.failed)
	require.Equal(t, "reauthorized-secret", repo.account.Credentials["api_key"])
}

func TestCursorSharedRefreshStopsAfterPersistenceConflictOrFailure(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			account := cursorCandidateAccount("composer-2.5")
			account.Status, account.Schedulable = StatusActive, true
			account.Credentials["api_key"] = cursorRefreshTestToken(time.Now().Add(time.Minute))
			oldToken := account.GetCredential("api_key")
			repo := &cursorRefreshTestRepo{account: account}
			if stale {
				repo.beforePersist = func() { repo.account.Credentials["api_key"] = "reauthorized-secret" }
			} else {
				repo.persistError = fmt.Errorf("database unavailable")
			}
			calls := 0
			upstream := &cursorRefreshTestUpstream{do: func(*http.Request, string, int64, int) (*http.Response, error) {
				calls++
				freshToken := cursorRefreshTestToken(time.Now().Add(60 * 24 * time.Hour))
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q}`, freshToken)))}, nil
			}}
			svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{TokenRefresh: config.TokenRefreshConfig{RefreshBeforeExpiryHours: 1, MaxRetries: 3}}, nil)
			defer svc.Stop()
			svc.SetCursorRefreshUpstream(upstream)
			svc.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil))
			svc.processRefreshContext(t.Context())
			require.Equal(t, 1, calls, "an accepted renewal must not be repeated after local persistence fails")
			require.Zero(t, repo.persisted)
			require.Zero(t, repo.failed, "persistence failure is not evidence of revoked credentials")
			if stale {
				require.Equal(t, "reauthorized-secret", repo.account.GetCredential("api_key"))
			} else {
				require.Equal(t, oldToken, repo.account.GetCredential("api_key"))
			}
		})
	}
}

func TestCursorRefreshExcludesOrdinaryAPIKeysAndEdgeMirrors(t *testing.T) {
	r := NewCursorTokenRefresher(nil)
	account := cursorCandidateAccount("composer-2.5")
	account.Credentials["api_key"] = cursorRefreshTestToken(time.Now().Add(time.Minute))
	require.True(t, r.NeedsRefresh(account, time.Hour))
	account.Credentials["base_url"] = "https://api-us3.tokenkey.dev"
	require.False(t, r.CanRefresh(account))
	delete(account.Extra, CursorSourceExtraKey)
	require.False(t, r.CanRefresh(account))
}
