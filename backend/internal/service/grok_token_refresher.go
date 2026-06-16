package service

import (
	"context"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// GrokTokenRefresher handles xAI / Grok (seventh platform) OAuth token refresh.
//
// xAI OAuth refresh is dependency-free (the network call lives in the stdlib-only
// pkg/xai helper), so — like KiroTokenRefresher — this refresher is constructed
// inline in NewTokenRefreshService with no Wire provider.
//
// On a non-2xx refresh, xai.RefreshToken returns an error whose text includes the
// upstream body; an "invalid_grant" (dead grant) is recognized by
// isNonRetryableRefreshError and the account is moved to error (re-auth required),
// matching the Anthropic OAuth rolling-revocation posture — so this Refresh just
// passes the error through.
type GrokTokenRefresher struct{}

// NewGrokTokenRefresher creates a Grok token refresher.
func NewGrokTokenRefresher() *GrokTokenRefresher {
	return &GrokTokenRefresher{}
}

// grokDefaultExpiresIn is the conservative access-token lifetime assumed when xAI
// omits expires_in, so NeedsRefresh still fires on a sane cadence.
const grokDefaultExpiresIn = 3600

// CacheKey returns the distributed-lock cache key.
func (r *GrokTokenRefresher) CacheKey(account *Account) string {
	return "grok:account:" + strconv.FormatInt(account.ID, 10)
}

// CanRefresh handles only grok-platform oauth-type accounts.
func (r *GrokTokenRefresher) CanRefresh(account *Account) bool {
	return account.Platform == PlatformGrok && account.Type == AccountTypeOAuth
}

// NeedsRefresh decides whether the access token is within the refresh window.
func (r *GrokTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	exp := account.GetGrokExpiresAt()
	if exp == nil {
		// No expiry recorded yet (e.g. fresh paste): refresh proactively so we
		// learn the real expiry and validate the grant.
		return true
	}
	return time.Until(*exp) < refreshWindow
}

// Refresh exchanges the stored refresh_token for a fresh access token, preserving
// all other credential fields and only updating the token-related ones.
func (r *GrokTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	res, err := xai.RefreshToken(ctx, account.GetGrokRefreshToken(), account.GetGrokTokenEndpoint(), proxyURL)
	if err != nil {
		return nil, err
	}

	expiresIn := res.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = grokDefaultExpiresIn
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second).Unix()

	newCreds := map[string]any{
		"access_token": res.AccessToken,
		"expires_at":   strconv.FormatInt(expiresAt, 10),
	}
	// xAI rotates the refresh_token but may omit it — keep the old one if absent.
	if res.RefreshToken != "" {
		newCreds["refresh_token"] = res.RefreshToken
	}
	// Cache the resolved token_endpoint to skip discovery on subsequent refreshes.
	if res.TokenEndpoint != "" {
		newCreds["token_endpoint"] = res.TokenEndpoint
	}

	return MergeCredentials(account.Credentials, newCreds), nil
}
