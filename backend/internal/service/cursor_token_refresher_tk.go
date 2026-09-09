package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
)

type CursorTokenRefresher struct{ upstream HTTPUpstream }

const cursorOAuthReauthorizationRequired = "Token refresh failed (non-retryable): browser reauthorization required"

func NewCursorTokenRefresher(upstream HTTPUpstream) *CursorTokenRefresher {
	return &CursorTokenRefresher{upstream: upstream}
}

func (r *CursorTokenRefresher) CacheKey(account *Account) string {
	return "cursor:account:" + strconv.FormatInt(account.ID, 10)
}

func (r *CursorTokenRefresher) CanRefresh(account *Account) bool {
	return account.IsCursor() && !isEdgeMirrorStub(account, edgeIDPattern) &&
		account.GetCredential("api_key") != ""
}

func (r *CursorTokenRefresher) NeedsRefresh(account *Account, window time.Duration) bool {
	if !r.CanRefresh(account) {
		return false
	}
	expires, err := cursor.OAuthTokenExpiry(account.GetCredential("api_key"))
	return err == nil && time.Until(expires) < window
}

func (r *CursorTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if !r.CanRefresh(account) || r.upstream == nil {
		return nil, errors.New("cursor token refresh is unavailable")
	}
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	result, err := cursor.RefreshOAuth(ctx, account.GetCredential("api_key"), func(req *http.Request) (*http.Response, error) {
		req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(req.Context()))
		return r.upstream.Do(req, proxyURL, account.ID, account.Concurrency)
	})
	if err != nil {
		return nil, err
	}
	return MergeCredentials(account.Credentials, map[string]any{
		"api_key":    result.AccessToken,
		"expires_at": strconv.FormatInt(result.ExpiresAt.Unix(), 10),
	}), nil
}

// Register before Start: the production wire provider supplies the same
// account-aware transport used by the gateway, including proxy/redirect policy.
func (s *TokenRefreshService) SetCursorRefreshUpstream(upstream HTTPUpstream) {
	r := NewCursorTokenRefresher(upstream)
	s.registrations = append(s.registrations, tokenRefreshRegistration{platform: PlatformNewAPI, refresher: r, executor: r})
}

type cursorOAuthCredentialsRepository interface {
	UpdateCursorOAuthCredentialsIfUnchanged(context.Context, int64, map[string]any, *int64, map[string]any, time.Time) (bool, error)
	SetCursorOAuthRefreshFailureIfUnchanged(context.Context, int64, map[string]any, *int64, string, *time.Time) (bool, error)
}

func (s *TokenRefreshService) recordCursorRefreshFailure(ctx context.Context, account *Account, cause error, until *time.Time) error {
	conditional, ok := s.accountRepo.(cursorOAuthCredentialsRepository)
	if !ok {
		return errors.New("cursor OAuth conditional persistence is unavailable")
	}
	reason := cursorOAuthReauthorizationRequired
	if until != nil {
		reason = "token refresh retry exhausted: cursor token renewal unavailable"
	}
	applied, err := conditional.SetCursorOAuthRefreshFailureIfUnchanged(ctx, account.ID, account.Credentials, account.ProxyID, reason, until)
	if err != nil {
		return err
	}
	if !applied {
		return errRefreshSkipped
	}
	blockedUntil := time.Time{}
	if until != nil {
		blockedUntil = *until
	}
	s.notifyAccountSchedulingBlocked(account, blockedUntil, reason)
	return cause
}

func persistCursorOAuthCredentials(ctx context.Context, repo AccountRepository, account *Account, credentials map[string]any) error {
	conditional, ok := repo.(cursorOAuthCredentialsRepository)
	if !ok {
		return errors.New("cursor OAuth conditional persistence is unavailable")
	}
	token, _ := credentials["api_key"].(string)
	expires, err := cursor.OAuthTokenExpiry(token)
	if err != nil {
		return err
	}
	credentials = FinalizeAccountCredentials(shallowCopyMap(credentials), account.ChannelType)
	applied, err := conditional.UpdateCursorOAuthCredentialsIfUnchanged(ctx, account.ID, account.Credentials, account.ProxyID, credentials, expires)
	if err != nil {
		return err
	}
	if !applied {
		return errRefreshSkipped
	}
	fresh, err := repo.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if fresh == nil {
		return ErrAccountNotFound
	}
	*account = *fresh
	return nil
}
